import { ApiClient } from '../client';
import { MobileTracing, tracingOrigin } from '../../tracing/mobile-tracing';

jest.mock('expo-crypto', () => {
  let sequence = 0;
  return { getRandomValues: (bytes: Uint8Array) => bytes.fill(++sequence) };
});

const host = 'https://example.test';
const id = '0bb58342-734e-4587-a1bd-95f9c1e7a302';
const traceparent = '00-12345678123456781234567812345678-1234567812345678-01';
const receipt = 'A'.repeat(86);
const nextTask = () => new Promise<void>((resolve) => setTimeout(resolve, 0));

it('exports linked history attempts without putting credentials and request data in spans', async () => {
  const upload = jest.fn<Promise<Response>, [string, RequestInit?]>()
    .mockRejectedValue(new Error('collector unavailable'));
  const request = jest.fn<Promise<Response>, [string, RequestInit?]>()
    .mockResolvedValueOnce(new Response(null, { status: 401 }))
    .mockResolvedValueOnce(new Response('{"items":[]}', {
      headers: { 'X-MW-Traceparent': traceparent, 'X-MW-Trace-Receipt': receipt },
    }));
  const getAccess = () => ({ token: 'secret-access', generation: 1 });
  const tracing = new MobileTracing({ origin: host, getAccess, fetch: upload, version: '1.0.0' });
  const api = new ApiClient({
    baseUrl: host, fetch: request, tracing, session: { getAccess, refreshAccess: async () => getAccess() },
  });
  await expect(api.getTelemetry(id, { cursor: 'private-cursor', traceHistory: true })).resolves.toEqual({ items: [] });
  expect(request).toHaveBeenCalledTimes(2);
  expect(request.mock.calls[0][1]?.headers).toMatchObject({ 'X-MW-Trace': '1' });
  expect(upload).not.toHaveBeenCalled();
  await nextTask();
  expect(upload).toHaveBeenCalledTimes(1);
  const [url, init] = upload.mock.calls[0];
  expect(url).toBe(host + '/v1/mobile/traces');
  expect(init?.headers).toMatchObject({
    Authorization: 'Bearer secret-access',
    'X-MW-Trace-Receipts': receipt,
    'Content-Type': 'application/x-protobuf',
  });
  const wire = new TextDecoder().decode(init?.body as ArrayBuffer);
  expect(wire).toContain('machine.history.load');
  expect(wire).toContain('GET /v1/machines/{machine_id}/telemetry');
  for (const privateData of ['secret-access', 'private-cursor', id, receipt]) {
    expect(wire).not.toContain(privateData);
  }
});

it('skips unsampled successful history calls and exports a bounded network error', async () => {
  const upload = jest.fn<Promise<Response>, [string, RequestInit?]>()
    .mockResolvedValue(new Response(null));
  const request = jest.fn<Promise<Response>, [string, RequestInit?]>()
    .mockResolvedValueOnce(new Response('{"items":[]}'))
    .mockRejectedValueOnce(new Error('private backend error'));
  const getAccess = () => ({ token: 'access', generation: 1 });
  const tracing = new MobileTracing({ origin: host, getAccess, fetch: upload });
  const api = new ApiClient({ baseUrl: host, fetch: request, tracing, session: { getAccess, refreshAccess: jest.fn() } });
  await api.getTelemetry(id, { traceHistory: true });
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
    fetch: async () => {
      generation += 1;
      return new Response('{"items":[]}', {
        headers: { 'X-MW-Traceparent': traceparent, 'X-MW-Trace-Receipt': receipt },
      });
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
