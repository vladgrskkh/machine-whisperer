import {
  isSpanContextValid, ROOT_CONTEXT, SpanKind, SpanStatusCode, trace, TraceFlags,
  type Context, type Span, type SpanContext, type Tracer,
} from '@opentelemetry/api';
import { ProtobufTraceSerializer } from '@opentelemetry/otlp-transformer';
import { resourceFromAttributes } from '@opentelemetry/resources';
import { TracerProvider, type ReadableSpan } from '@opentelemetry/sdk-trace';
import { getRandomValues } from 'expo-crypto';
import { fetch as expoFetch } from 'expo/fetch';

import { ApiError } from '../api/errors';
import { bestEffort } from './best-effort';

type Access = { token: string; generation: number };
type Failure = 'network_error' | 'timeout' | 'cancelled' | 'http_error' | 'invalid_response';

type Options = {
  origin: string;
  getAccess: () => Access | null;
  version?: string;
  fetch?: (url: string, init?: RequestInit) => Promise<unknown>;
};

type Action = {
  generation: number;
  spans: ReadableSpan[];
  receipts: Set<string>;
  networkFailure: boolean;
};

const route = '/v1/machines/{machine_id}/telemetry';
const maxActiveActions = 8;
const maxQueuedActions = 4;
const maxBatchSpans = 16;
const maxExportBytes = 32 * 1024;
const exportTimeout = 2_000;
const traceparent = /^00-([a-f0-9]{32})-([a-f0-9]{16})-01$/;
const receiptPattern = /^[A-Za-z0-9_-]{86}$/;

export function tracingOrigin(baseUrl?: string, browserOrigin?: string): string | null {
  try {
    const url = new URL(baseUrl?.trim() ?? '');
    if (url.protocol !== 'https:' || url.username || url.password || url.pathname !== '/' || url.search ||
      url.hash || (browserOrigin !== undefined && url.origin !== browserOrigin)) return null;
    return url.origin;
  } catch {
    return null;
  }
}

export function createMobileTracing(options: Options): MobileTracing | undefined {
  try {
    return new MobileTracing(options);
  } catch {
    return undefined;
  }
}

function randomId(size: number): string {
  const bytes = getRandomValues(new Uint8Array(size));
  const id = Array.from(bytes, (value) => value.toString(16).padStart(2, '0')).join('');
  if (/^0+$/.test(id)) throw new Error('Trace ID entropy unavailable');
  return id;
}

function linkedContext(header: string | null): SpanContext | null {
  const match = header?.match(traceparent);
  if (!match) return null;
  const context = { traceId: match[1], spanId: match[2], traceFlags: TraceFlags.SAMPLED, isRemote: true };
  return isSpanContextValid(context) ? context : null;
}

function fail(span: Span, type: Failure) {
  span.setStatus({ code: SpanStatusCode.ERROR });
  span.setAttribute('error.type', type);
}

function actionFailure(error: unknown): Failure {
  if (!(error instanceof ApiError)) return 'network_error';
  switch (error.code) {
    case 'network_error': return 'network_error';
    case 'request_cancelled': return 'cancelled';
    case 'invalid_response': return 'invalid_response';
    default: return 'http_error';
  }
}

export class MobileTracing {
  private readonly tracer: Tracer;
  private readonly actions = new Map<string, Action>();
  private readonly queue: Action[] = [];
  private draining = false;

  constructor(private readonly options: Options) {
    const provider = new TracerProvider({
      resource: resourceFromAttributes({
        'service.name': 'mobile',
        'service.namespace': 'machine-whisperer',
        'service.version': options.version,
      }),
      idGenerator: { generateTraceId: () => randomId(16), generateSpanId: () => randomId(8) },
      spanLimits: { linkCountLimit: 1, eventCountLimit: 0, attributeCountLimit: 8 },
      spanProcessors: [{
        onStart() {},
        onEnd: (span) => {
          const action = this.actions.get(span.spanContext().traceId);
          if (action && action.spans.length < maxBatchSpans) action.spans.push(span);
        },
        async forceFlush() {},
        async shutdown() {},
      }],
    });
    this.tracer = provider.getTracer('machine-whisperer.mobile');
  }

  async history<T>(operation: (context?: Context) => Promise<T>): Promise<T> {
    const state = bestEffort(() => {
      const access = this.options.getAccess();
      if (!access || this.actions.size >= maxActiveActions) return undefined;
      const span = this.tracer.startSpan('machine.history.load', { kind: SpanKind.INTERNAL }, ROOT_CONTEXT);
      const id = span.spanContext().traceId;
      const context = trace.setSpan(ROOT_CONTEXT, span);
      const action: Action = { generation: access.generation, spans: [], receipts: new Set(), networkFailure: false };
      this.actions.set(id, action);
      return { span, id, action, context };
    });
    if (!state) return operation();
    const { span, id, action, context } = state;
    try {
      return await operation(context);
    } catch (error) {
      bestEffort(() => fail(span, actionFailure(error)));
      throw error;
    } finally {
      bestEffort(() => span.end());
      this.actions.delete(id);
      if ((action.receipts.size > 0 || action.networkFailure) && action.spans.length > 0) {
        bestEffort(() => this.enqueue(action));
      }
    }
  }

  attempt(context: Context, token: string | undefined) {
    const id = trace.getSpanContext(context)?.traceId;
    const action = id ? this.actions.get(id) : undefined;
    const access = this.options.getAccess();
    if (!action || access?.generation !== action.generation || access.token !== token) return undefined;
    let span: Span;
    try {
      span = this.tracer.startSpan('GET ' + route, {
        kind: SpanKind.CLIENT,
        attributes: { 'http.request.method': 'GET', 'http.route': route },
      }, context);
    } catch {
      return undefined;
    }
    return {
      response: (response: Response) => {
        span.setAttribute('http.response.status_code', response.status);
        if (response.status >= 400) fail(span, 'http_error');
        const link = linkedContext(response.headers.get('X-MW-Traceparent'));
        const receipt = response.headers.get('X-MW-Trace-Receipt');
        if (link && receipt && receiptPattern.test(receipt)) {
          span.addLink({ context: link });
          action.receipts.add(receipt);
        }
      },
      fail: (type: Failure) => {
        fail(span, type);
        if (type === 'network_error' || type === 'timeout') action.networkFailure = true;
      },
      end: () => span.end(),
    };
  }

  private enqueue(action: Action) {
    if (this.queue.length >= maxQueuedActions) this.queue.shift();
    this.queue.push(action);
    this.scheduleDrain();
  }

  private scheduleDrain() {
    if (this.draining || this.queue.length === 0) return;
    this.draining = true;
    // Let the history promise settle before serializing or sending telemetry.
    if (bestEffort(() => setTimeout(() => { void this.drain(); }, 0)) === undefined) this.draining = false;
  }

  private async drain() {
    try {
      const action = this.queue.shift();
      if (action) await this.send(action);
    } finally {
      this.draining = false;
      this.scheduleDrain();
    }
  }

  private async send(action: Action) {
    let timeout: ReturnType<typeof setTimeout> | undefined;
    try {
      const access = this.options.getAccess();
      if (!access || access.generation !== action.generation) return;
      const data = ProtobufTraceSerializer.serializeRequest(action.spans);
      if (!data || data.byteLength > maxExportBytes) return;
      const controller = new AbortController();
      timeout = setTimeout(() => controller.abort(), exportTimeout);
      const fetch = this.options.fetch ?? expoFetch;
      await fetch(this.options.origin + '/v1/mobile/traces', {
        method: 'POST',
        body: data.slice().buffer,
        signal: controller.signal,
        redirect: 'error',
        headers: {
          Authorization: 'Bearer ' + access.token,
          'Content-Type': 'application/x-protobuf',
          'X-MW-Trace-Receipts': Array.from(action.receipts).join(','),
        },
      });
    } catch {
      // An unavailable telemetry path leaves app requests and sessions independent.
    } finally {
      if (timeout !== undefined) clearTimeout(timeout);
    }
  }
}
