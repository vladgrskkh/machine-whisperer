import * as SecureStore from 'expo-secure-store';

import { ApiClient } from '../../api/client';
import type { TokenPair } from '../../api/types';
import { secureTokenStore } from '../secure-token-store';
import { Session } from '../session';

jest.mock('expo-secure-store', () => ({
  getItemAsync: jest.fn(),
  setItemAsync: jest.fn(),
  deleteItemAsync: jest.fn(),
  WHEN_UNLOCKED_THIS_DEVICE_ONLY: 'WHEN_UNLOCKED_THIS_DEVICE_ONLY',
}));

const initial: TokenPair = {
  access_token: 'old-access', refresh_token: 'old-refresh', token_type: 'Bearer', expires_in: 900,
};
const rotated: TokenPair = {
  access_token: 'new-access', refresh_token: 'new-refresh', token_type: 'Bearer', expires_in: 900,
};
const user = { id: 'operator-id', email: 'operator@example.com', created_at: '2026-11-24T00:00:00Z' };

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), { status });
}

function unauthorized() {
  return json({ error: { code: 'authentication_failed', message: 'Authentication failed' } }, 401);
}

function deferred() {
  let resolve!: () => void;
  const promise = new Promise<void>((done) => { resolve = done; });
  return { promise, resolve };
}

function setup(request: (url: string, init?: RequestInit) => Promise<Response>) {
  const options = { baseUrl: 'https://example.test/', fetch: request };
  const session = new Session(secureTokenStore, new ApiClient(options));
  return { session, api: new ApiClient({ ...options, session }) };
}

let storedValue: string | null;

beforeEach(() => {
  storedValue = JSON.stringify(initial);
  jest.mocked(SecureStore.getItemAsync).mockImplementation(async () => storedValue);
  jest.mocked(SecureStore.setItemAsync).mockImplementation(async (_key, value) => { storedValue = value; });
  jest.mocked(SecureStore.deleteItemAsync).mockImplementation(async () => { storedValue = null; });
});

it('rehydrates a secure session and sends the stored access token', async () => {
  const request = jest.fn(async () => json(user));
  const { api, session } = setup(request);

  await session.initialize();
  await expect(api.me()).resolves.toEqual(user);
  expect(session.getStatus()).toBe('signedIn');
  expect(request).toHaveBeenCalledWith('https://example.test/v1/users/me', expect.objectContaining({
    headers: expect.objectContaining({ Authorization: 'Bearer old-access' }),
  }));
});

it('rotates once for concurrent 401s and persists the pair before either retry', async () => {
  const allOldRequestsStarted = deferred();
  const refreshStarted = deferred();
  const allowRotation = deferred();
  const allowLate401 = deferred();
  let unauthorizedRequests = 0;
  const refreshTokens: string[] = [];
  const request: typeof fetch = async (url, options) => {
    if (String(url).endsWith('/refresh')) {
      refreshTokens.push(JSON.parse(String(options?.body)).refresh_token);
      refreshStarted.resolve();
      await allowRotation.promise;
      return json(rotated);
    }
    if ((options?.headers as Record<string, string>).Authorization === 'Bearer old-access') {
      unauthorizedRequests += 1;
      if (unauthorizedRequests === 2) {
        allOldRequestsStarted.resolve();
        await allowLate401.promise;
      }
      return unauthorized();
    }
    expect(JSON.parse(storedValue!)).toEqual(rotated);
    return json(user);
  };
  const { api, session } = setup(request);
  await session.initialize();

  const first = api.me();
  const late = api.me();
  await Promise.all([allOldRequestsStarted.promise, refreshStarted.promise]);
  allowRotation.resolve();
  await expect(first).resolves.toEqual(user);
  allowLate401.resolve();
  await expect(late).resolves.toEqual(user);
  expect(refreshTokens).toEqual(['old-refresh']);
});

it('joins a refresh already in flight when another request receives a 401', async () => {
  const request = jest.fn(async () => unauthorized());
  const { session } = setup(request);
  await session.initialize();
  const grant = session.getAccess()!;
  const first = session.refreshAccess(grant);
  const second = session.refreshAccess(grant);

  await Promise.allSettled([first, second]);
  expect(request).toHaveBeenCalledTimes(1);
  expect(session.getStatus()).toBe('signedOut');
  expect(storedValue).toBeNull();
});

it('clears a rejected refresh and prevents additional requests with stale credentials', async () => {
  const request = jest.fn(async () => unauthorized());
  const { api, session } = setup(request);
  await session.initialize();

  await expect(api.me()).rejects.toMatchObject({ status: 401, code: 'authentication_failed' });
  await expect(api.me()).rejects.toMatchObject({ status: 401, code: 'session_required' });
  expect(request).toHaveBeenCalledTimes(2);
  expect(session.getStatus()).toBe('signedOut');
  expect(storedValue).toBeNull();
});

it('drops a refresh token when the network outcome is uncertain', async () => {
  const request = jest.fn(async (url: string) => {
    if (String(url).endsWith('/refresh')) throw new TypeError('Network unavailable');
    return unauthorized();
  });
  const { api, session } = setup(request);
  await session.initialize();

  await expect(api.me()).rejects.toMatchObject({ code: 'network_error' });
  await expect(api.me()).rejects.toMatchObject({ code: 'session_required' });
  expect(request).toHaveBeenCalledTimes(2);
  expect(storedValue).toBeNull();
});

it('sends the refresh token on logout and removes the secure session even if offline', async () => {
  const request = jest.fn(async () => { throw new TypeError('Network unavailable'); });
  const { session, api } = setup(request);
  await session.initialize();

  await session.logout();
  expect(request).toHaveBeenCalledWith('https://example.test/v1/auth/logout', expect.objectContaining({
    method: 'POST', body: JSON.stringify({ refresh_token: 'old-refresh' }),
  }));
  expect(session.getStatus()).toBe('signedOut');
  expect(storedValue).toBeNull();
  await expect(api.me()).rejects.toMatchObject({ code: 'session_required' });
});

it('does not save a late rotated pair after logout', async () => {
  const refreshStarted = deferred();
  const allowRotation = deferred();
  const request: typeof fetch = async (url) => {
    if (String(url).endsWith('/logout')) return new Response(null, { status: 204 });
    if (String(url).endsWith('/refresh')) {
      refreshStarted.resolve();
      await allowRotation.promise;
      return json(rotated);
    }
    return unauthorized();
  };
  const { session, api } = setup(request);
  await session.initialize();

  const pending = expect(api.me()).rejects.toMatchObject({ code: 'session_required' });
  await refreshStarted.promise;
  await session.logout();
  allowRotation.resolve();
  await pending;
  expect(session.getStatus()).toBe('signedOut');
  expect(storedValue).toBeNull();
});

it('preserves a successful sign-in if an overlapping sign-in fails', async () => {
  storedValue = null;
  const saving = deferred();
  const allowSave = deferred();
  jest.mocked(SecureStore.setItemAsync).mockImplementation(async (_key, value) => {
    saving.resolve();
    await allowSave.promise;
    storedValue = value;
  });
  const request = jest.fn()
    .mockImplementationOnce(async () => json(initial))
    .mockImplementationOnce(async () => unauthorized());
  const { session } = setup(request);
  const valid = { email: 'operator@example.com', password: 'an operator password' };
  const invalid = { ...valid, password: 'incorrect password' };
  const first = session.login(valid);
  await saving.promise;
  await expect(session.login(invalid)).rejects.toMatchObject({ code: 'authentication_failed' });
  allowSave.resolve();

  await expect(first).resolves.toBeUndefined();
  expect(session.getStatus()).toBe('signedIn');
  expect(JSON.parse(storedValue!)).toEqual(initial);
});

it('cannot restore a predecessor token while its refresh response is pending', async () => {
  const refreshStarted = deferred();
  const releaseResponse = deferred();
  const request: typeof fetch = async (url) => {
    if (!String(url).endsWith('/refresh')) return unauthorized();
    refreshStarted.resolve();
    await releaseResponse.promise;
    return json(rotated);
  };
  const { session } = setup(request);
  await session.initialize();
  const refreshing = session.refreshAccess(session.getAccess()!);
  await refreshStarted.promise;

  const { session: restarted } = setup(request);
  await restarted.initialize();
  try {
    expect(restarted.getStatus()).toBe('signedOut');
    expect(restarted.getAccess()).toBeNull();
  } finally {
    releaseResponse.resolve();
    await refreshing;
  }
});

it('keeps access disabled after failed logout cleanup and removes the session on retry', async () => {
  jest.mocked(SecureStore.deleteItemAsync).mockRejectedValueOnce(new Error('Secure storage unavailable'));
  const request = jest.fn(async () => { throw new TypeError('Network unavailable'); });
  const { session, api } = setup(request);
  await session.initialize();

  await expect(session.logout()).rejects.toMatchObject({ code: 'secure_storage_error' });
  expect(session.getStatus()).toBe('cleanupFailed');
  await expect(api.me()).rejects.toMatchObject({ code: 'session_required' });

  await session.logout();
  const { session: restarted } = setup(request);
  await restarted.initialize();
  expect(session.getStatus()).toBe('signedOut');
  expect(restarted.getStatus()).toBe('signedOut');
  expect(request).toHaveBeenCalledTimes(1);
});

it.each(['initial', 'retry'])('discards a successful %s request after the session changes', async (stage) => {
  const successStarted = deferred();
  const releaseSuccess = deferred();
  const request: typeof fetch = async (url, options) => {
    const path = String(url);
    if (path.endsWith('/refresh')) return json(rotated);
    if (path.endsWith('/logout')) return new Response(null, { status: 204 });
    if (path.endsWith('/login')) return json({
      ...rotated, access_token: 'another-access', refresh_token: 'another-refresh',
    });
    if (stage === 'retry' && (options?.headers as Record<string, string>).Authorization === 'Bearer old-access') {
      return unauthorized();
    }
    const response = json(user);
    const readBody = response.json.bind(response);
    response.json = async () => {
      successStarted.resolve();
      await releaseSuccess.promise;
      return readBody();
    };
    return response;
  };
  const { api, session } = setup(request);
  await session.initialize();
  const result = api.me();
  await successStarted.promise;

  await session.logout();
  await session.login({ email: 'another@example.com', password: 'another password' });
  releaseSuccess.resolve();

  await expect(result).rejects.toMatchObject({ code: 'session_required' });
  expect(session.getStatus()).toBe('signedIn');
});

it('does not dispatch refresh if SecureStore cannot remove the predecessor', async () => {
  jest.mocked(SecureStore.deleteItemAsync).mockRejectedValueOnce(new Error('Secure storage unavailable'));
  const request = jest.fn(async () => json(rotated));
  const { session } = setup(request);
  await session.initialize();

  await expect(session.refreshAccess(session.getAccess()!)).rejects.toMatchObject({ code: 'secure_storage_error' });
  expect(request).not.toHaveBeenCalled();
  expect(session.getStatus()).toBe('signedOut');
  expect(storedValue).toBeNull();
});
