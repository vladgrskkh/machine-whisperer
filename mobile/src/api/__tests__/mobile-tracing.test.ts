import { ProtobufTraceSerializer } from '@opentelemetry/otlp-transformer';

import { ApiClient } from '../client';
import { MobileTracing, tracingOrigin } from '../../tracing/mobile-tracing';

jest.mock('expo-crypto', () => {
  let sequence = 0;
  return { getRandomValues: (bytes: Uint8Array) => bytes.fill(++sequence) };
});

const host = 'https://example.test';
const id = '0bb58342-734e-4587-a1bd-95f9c1e7a302';
const nextTask = () => new Promise<void>((resolve) => setTimeout(resolve, 0));

function sampledHistory(init?: RequestInit) {
  const traceId = new Headers(init?.headers).get('traceparent')?.split('-')[1] ?? '';
  return new Response('{"items":[]}', { headers: { 'X-Mw-Sampled-Trace': traceId } });
}

it('exports mobile root and HTTP children on the edge-sampled W3C trace', async () => {
  const upload = jest.fn<Promise<Response>, [string, RequestInit?]>()
    .mockRejectedValue(new Error('collector unavailable'));
  const request = jest.fn<Promise<Response>, [string, RequestInit?]>()
    .mockResolvedValueOnce(new Response(null, { status: 401 }))
    .mockImplementationOnce(async (_url, init) => sampledHistory(init));
  const getAccess = () => ({ token: 'secret-access', generation: 1 });
  const tracing = new MobileTracing({ origin: host, getAccess, fetch: upload, version: '1.0.0' });
  const api = new ApiClient({
    baseUrl: host, fetch: request, tracing, session: { getAccess, refreshAccess: async () => getAccess() },
  });
  const serialize = jest.spyOn(ProtobufTraceSerializer, 'serializeRequest');
  try {
    await expect(api.getTelemetry(id, { cursor: 'private-cursor', traceHistory: true })).resolves.toEqual({ items: [] });
    expect(request).toHaveBeenCalledTimes(2);
    expect(new Headers(request.mock.calls[0][1]?.headers).get('traceparent'))
      .toMatch(/^00-[a-f0-9]{32}-[a-f0-9]{16}-01$/);
    expect(serialize).not.toHaveBeenCalled();
    expect(upload).not.toHaveBeenCalled();
    await nextTask();
    expect(upload).toHaveBeenCalledTimes(1);
    const [spans] = serialize.mock.calls[0];
    const root = spans.find((span) => span.name === 'machine.history.load');
    expect(root).toBeDefined();
    if (!root) throw new Error('Missing history root');
    expect(root.parentSpanContext).toBeUndefined();
    const children = spans.filter((span) => span.name === 'GET /v1/machines/{machine_id}/telemetry');
    expect(children).toHaveLength(2);
    for (const [index, child] of children.entries()) {
      const { traceId, spanId } = child.spanContext();
      expect(traceId).toBe(root.spanContext().traceId);
      expect(child.parentSpanContext?.spanId).toBe(root.spanContext().spanId);
      expect(new Headers(request.mock.calls[index][1]?.headers).get('traceparent'))
        .toBe('00-' + traceId + '-' + spanId + '-01');
    }
    const [url, init] = upload.mock.calls[0];
    expect(url).toBe(host + '/otlp/v1/traces');
    expect(init?.headers).toMatchObject({
      Authorization: 'Bearer secret-access',
      'Content-Type': 'application/x-protobuf',
    });
    const wire = new TextDecoder().decode(init?.body as ArrayBuffer);
    for (const privateData of ['secret-access', 'private-cursor', id]) {
      expect(wire).not.toContain(privateData);
    }
  } finally {
    serialize.mockRestore();
  }
});

it('skips unsampled successful history calls and exports a bounded network error', async () => {
  const upload = jest.fn<Promise<Response>, [string, RequestInit?]>()
    .mockResolvedValue(new Response(null));
  const request = jest.fn<Promise<Response>, [string, RequestInit?]>()
    .mockResolvedValueOnce(new Response('{"items":[]}'))
    .mockResolvedValueOnce(new Response('{"items":[]}', {
      headers: { 'X-Mw-Sampled-Trace': 'f'.repeat(32) },
    }))
    .mockRejectedValueOnce(new Error('private backend error'));
  const getAccess = () => ({ token: 'access', generation: 1 });
  const tracing = new MobileTracing({ origin: host, getAccess, fetch: upload });
  const api = new ApiClient({ baseUrl: host, fetch: request, tracing, session: { getAccess, refreshAccess: jest.fn() } });
  await api.getTelemetry(id, { traceHistory: true });
  await api.getTelemetry(id, { traceHistory: true });
  await nextTask();
  expect(upload).not.toHaveBeenCalled();
  await expect(api.getTelemetry(id, { traceHistory: true })).rejects.toMatchObject({ code: 'network_error' });
  expect(upload).not.toHaveBeenCalled();
  await nextTask();
  expect(upload).toHaveBeenCalledTimes(1);
  const wire = new TextDecoder().decode(upload.mock.calls[0][1]?.body as ArrayBuffer);
  expect(wire).toContain('network_error');
  expect(wire).not.toContain('private backend error');
});

it('does not send an earlier account’s trace with the current session', async () => {
  let generation = 1;
  const getAccess = () => ({ token: 'access-' + generation, generation });
  const upload = jest.fn();
  const tracing = new MobileTracing({ origin: host, getAccess, fetch: upload });
  const api = new ApiClient({
    baseUrl: host,
    session: { getAccess, refreshAccess: jest.fn() },
    tracing,
    fetch: async (_url, init) => {
      generation += 1;
      return sampledHistory(init);
    },
  });
  await expect(api.getTelemetry(id, { traceHistory: true })).rejects.toMatchObject({ code: 'session_required' });
  await nextTask();
  expect(upload).not.toHaveBeenCalled();
});

it('allows web tracing for same-origin HTTPS only', () => {
  expect(tracingOrigin(host, host)).toBe(host);
  expect(tracingOrigin(host, 'https://other.test')).toBeNull();
  expect(tracingOrigin('http://example.test', 'http://example.test')).toBeNull();
});

it('isolates failing request tracing and still cleans up the request', async () => {
  jest.useFakeTimers();
  try {
    const getAccess = () => ({ token: 'access', generation: 1 });
    const tracing = new MobileTracing({ origin: host, getAccess });
    const broken = () => { throw new Error('trace callback error'); };
    jest.spyOn(tracing, 'attempt').mockImplementationOnce(broken).mockReturnValue({
      traceparent: '00-' + '1'.repeat(32) + '-' + '2'.repeat(16) + '-01',
      response: broken, fail: broken, end: broken,
    });
    const request = jest.fn<Promise<Response>, [string, RequestInit?]>()
      .mockResolvedValueOnce(new Response('{"items":[]}'))
      .mockResolvedValueOnce(new Response('{"items":[]}'))
      .mockResolvedValueOnce(new Response('{"error":{"code":"problem","message":"Unavailable"}}', { status: 502 }))
      .mockRejectedValueOnce(new Error('network'));
    const api = new ApiClient({ baseUrl: host, fetch: request, tracing, session: { getAccess, refreshAccess: jest.fn() } });
    const controller = new AbortController();
    const remove = jest.spyOn(controller.signal, 'removeEventListener');
    const options = { traceHistory: true, signal: controller.signal };
    await expect(api.getTelemetry(id, options)).resolves.toEqual({ items: [] });
    await expect(api.getTelemetry(id, options)).resolves.toEqual({ items: [] });
    await expect(api.getTelemetry(id, options)).rejects.toMatchObject({ status: 502, code: 'problem' });
    await expect(api.getTelemetry(id, options)).rejects.toMatchObject({ code: 'network_error' });
    expect(remove.mock.calls.map(([event]) => event)).toEqual(Array(4).fill('abort'));
    expect(jest.getTimerCount()).toBe(0);
  } finally {
    jest.useRealTimers();
  }
});
