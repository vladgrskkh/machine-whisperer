import { ApiError, decodeApiError, sessionRequired } from './errors';
import { isTokenPair, type Credentials, type TokenPair, type User } from './types';

export type AccessGrant = { token: string; generation: number };

type RequestSession = {
  getAccess(): AccessGrant | null;
  refreshAccess(grant: AccessGrant): Promise<AccessGrant>;
};

type Fetch = (url: string, init?: RequestInit) => Promise<Response>;

type ApiClientOptions = {
  baseUrl?: string;
  fetch?: Fetch;
  session?: RequestSession;
};

type JsonRequest = { method?: 'GET' | 'POST' | 'PATCH' | 'PUT' | 'DELETE'; body?: unknown };
type ApiPath = `/v1/${string}`;

export class ApiClient {
  private readonly baseUrl: string;
  private readonly fetch: Fetch;
  private readonly session?: RequestSession;

  constructor(options: ApiClientOptions) {
    this.baseUrl = options.baseUrl?.trim().replace(/\/+$/, '') ?? '';
    this.fetch = options.fetch ?? globalThis.fetch.bind(globalThis);
    this.session = options.session;
  }

  signup(credentials: Credentials): Promise<User> {
    return this.send('/v1/auth/signup', { method: 'POST', body: credentials });
  }

  login(credentials: Credentials): Promise<TokenPair> {
    return this.requestPair('/v1/auth/login', credentials);
  }

  refresh(refreshToken: string): Promise<TokenPair> {
    return this.requestPair('/v1/auth/refresh', { refresh_token: refreshToken });
  }

  logout(refreshToken: string): Promise<void> {
    return this.send('/v1/auth/logout', { method: 'POST', body: { refresh_token: refreshToken } });
  }

  me(): Promise<User> {
    return this.authenticated('/v1/users/me');
  }

  async authenticated<T>(path: ApiPath, request: JsonRequest = {}): Promise<T> {
    const session = this.session;
    const grant = session?.getAccess();
    if (!session || !grant) throw sessionRequired();

    try {
      return await this.send<T>(path, request, grant.token);
    } catch (error) {
      if (!(error instanceof ApiError) || error.status !== 401) throw error;
    }

    const renewed = await session.refreshAccess(grant);
    if (session.getAccess()?.generation !== renewed.generation) throw sessionRequired();
    return this.send<T>(path, request, renewed.token);
  }

  private async requestPair(path: ApiPath, body: unknown) {
    const value = await this.send<unknown>(path, { method: 'POST', body });
    if (!isTokenPair(value)) {
      throw new ApiError(0, 'invalid_response', 'The server returned an unreadable response.');
    }
    return value;
  }

  private async send<T>(path: ApiPath, request: JsonRequest, token?: string): Promise<T> {
    if (!this.baseUrl) {
      throw new ApiError(0, 'missing_api_url', 'The app needs an API address.');
    }

    const headers: Record<string, string> = { Accept: 'application/json' };
    if (request.body !== undefined) headers['Content-Type'] = 'application/json';
    if (token) headers.Authorization = `Bearer ${token}`;

    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), 15_000);
    try {
      const response = await this.fetch(`${this.baseUrl}${path}`, {
        method: request.method ?? 'GET',
        headers,
        body: request.body === undefined ? undefined : JSON.stringify(request.body),
        signal: controller.signal,
      });
      if (response.status === 204) return undefined as T;
      const body: unknown = await response.json().catch(() => undefined);
      if (!response.ok) throw decodeApiError(response.status, body);
      if (body === undefined) {
        throw new ApiError(response.status, 'invalid_response', 'The server returned an unreadable response.');
      }
      return body as T;
    } catch (error) {
      if (error instanceof ApiError) throw error;
      throw new ApiError(0, 'network_error', 'Could not reach the server. Check your connection and try again.');
    } finally {
      clearTimeout(timer);
    }
  }
}
