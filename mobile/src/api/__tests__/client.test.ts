import { ApiClient } from '../client';
import { ApiError } from '../errors';

const credential = { email: 'operator@example.com', password: 'a long password here' };

it('decodes the API public error envelope', async () => {
  const request: typeof fetch = async () => new Response(JSON.stringify({
    error: { code: 'authentication_failed', message: 'Incorrect email or password.' },
  }), { status: 401 });
  const api = new ApiClient({ baseUrl: 'https://example.test', fetch: request });

  try {
    await api.login(credential);
    throw new Error('Expected login to reject');
  } catch (error) {
    expect(error).toBeInstanceOf(ApiError);
    expect(error).toMatchObject({
      status: 401,
      code: 'authentication_failed',
      message: 'Incorrect email or password.',
    });
  }
});

it('uses a generic error if the response body is outside the public envelope', async () => {
  const request: typeof fetch = async () => new Response('private proxy details', { status: 502 });
  const api = new ApiClient({ baseUrl: 'https://example.test', fetch: request });

  await expect(api.signup(credential)).rejects.toMatchObject({
    status: 502,
    code: 'http_error',
    message: 'The server could not complete the request.',
  });
});

const machine = {
  id: '0bb58342-734e-4587-a1bd-95f9c1e7a302',
  name: 'Workshop fan',
  created_at: '2026-11-24T00:00:00Z',
};
const deviceId = '93a68d42-c83f-4221-aeae-d951a995b262';
const claimCode = 'A'.repeat(43);

it('maps machine and claim operations to authenticated API endpoints', async () => {
  const claimed = { device_id: deviceId, machine_id: machine.id, status: 'claimed' };
  const request = jest.fn<ReturnType<typeof fetch>, Parameters<typeof fetch>>()
    .mockResolvedValueOnce(new Response(JSON.stringify([machine])))
    .mockResolvedValueOnce(new Response(JSON.stringify(machine), { status: 201 }))
    .mockResolvedValueOnce(new Response(JSON.stringify(machine)))
    .mockResolvedValueOnce(new Response(null, { status: 204 }))
    .mockResolvedValueOnce(new Response(JSON.stringify(claimed)));
  const api = new ApiClient({
    baseUrl: 'https://example.test',
    fetch: request,
    session: { getAccess: () => ({ token: 'access', generation: 1 }), refreshAccess: jest.fn() },
  });

  await expect(api.listMachines()).resolves.toEqual([machine]);
  await expect(api.createMachine({ name: 'Workshop fan' })).resolves.toEqual(machine);
  await expect(api.getMachine(machine.id)).resolves.toEqual(machine);
  await expect(api.deleteMachine(machine.id)).resolves.toBeUndefined();
  const claim = { device_id: deviceId, machine_id: machine.id, claim_code: claimCode };
  await expect(api.claimDevice(claim)).resolves.toEqual(claimed);

  expect(request.mock.calls.map(([url, init]) => [
    url, init?.method, new Headers(init?.headers).get('Authorization'), init?.body && JSON.parse(String(init.body)),
  ])).toEqual([
    ['https://example.test/v1/machines', 'GET', 'Bearer access', undefined],
    ['https://example.test/v1/machines', 'POST', 'Bearer access', { name: 'Workshop fan' }],
    [`https://example.test/v1/machines/${machine.id}`, 'GET', 'Bearer access', undefined],
    [`https://example.test/v1/machines/${machine.id}`, 'DELETE', 'Bearer access', undefined],
    ['https://example.test/v1/devices/claim', 'POST', 'Bearer access', claim],
  ]);
});

it.each([
  null,
  { device_id: deviceId, machine_id: machine.id, status: 'unclaimed' },
  { device_id: deviceId, machine_id: deviceId, status: 'claimed' },
])('rejects a response that does not confirm the requested claim', async (body) => {
  const api = new ApiClient({
    baseUrl: 'https://example.test',
    fetch: async () => new Response(JSON.stringify(body)),
    session: { getAccess: () => ({ token: 'access', generation: 1 }), refreshAccess: jest.fn() },
  });
  await expect(api.claimDevice({ device_id: deviceId, machine_id: machine.id, claim_code: claimCode }))
    .rejects.toMatchObject({ code: 'invalid_response' });
});

const profile = {
  id: '31766072-1cfc-4798-ac7e-340151697e3c',
  email: 'operator@example.com',
  created_at: '2026-11-24T00:00:00.123456Z',
};

it.each([
  null,
  { ...profile, id: undefined },
  { ...profile, id: 'invalid' },
  { ...profile, email: null },
  { ...profile, email: '' },
  { ...profile, email: 'invalid' },
  { ...profile, created_at: 1795478400000 },
  { ...profile, created_at: 'invalid' },
  { ...profile, created_at: '123' },
  { ...profile, created_at: '2026-02-29T14:30:59+03:30' },
])('rejects malformed account responses (%j)', async (body) => {
  const api = new ApiClient({
    baseUrl: 'https://example.test',
    fetch: async () => new Response(JSON.stringify(body)),
    session: { getAccess: () => ({ token: 'access', generation: 1 }), refreshAccess: jest.fn() },
  });

  await expect(api.me()).rejects.toMatchObject({ code: 'invalid_response' });
});

it('accepts valid account responses with fractional seconds', async () => {
  const api = new ApiClient({
    baseUrl: 'https://example.test',
    fetch: async () => new Response(JSON.stringify(profile)),
    session: { getAccess: () => ({ token: 'access', generation: 1 }), refreshAccess: jest.fn() },
  });

  await expect(api.me()).resolves.toEqual(profile);
});
