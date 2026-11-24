import { ApiError, decodeApiError, sessionRequired } from './errors';
import {
  isTokenPair, isUser, type BaselineValues, type ClaimDeviceRequest, type ClaimedDevice, type CreateMachineRequest,
  type Credentials, type Machine, type MachineAlert, type MachineBaseline, type TelemetryPage, type TokenPair, type User,
} from './types';

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

type JsonRequest = {
  method?: 'GET' | 'POST' | 'PATCH' | 'PUT' | 'DELETE';
  body?: unknown;
  signal?: AbortSignal;
};
type ApiPath = `/v1/${string}`;
type TelemetryQuery = { cursor?: string | null; limit?: number; signal?: AbortSignal };

const maxMachineReads = 4;

function cancelledRequest() {
  return new ApiError(0, 'request_cancelled', 'The request was cancelled.');
}

export class ApiClient {
  private readonly baseUrl: string;
  private readonly fetch: Fetch;
  private readonly session?: RequestSession;
  private activeMachineReads = 0;
  private readonly waitingMachineReads: (() => void)[] = [];

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

  async me(): Promise<User> {
    const profile = await this.authenticated<unknown>('/v1/users/me');
    if (!isUser(profile)) throw new ApiError(0, 'invalid_response', 'The server returned an unreadable account response.');
    return profile;
  }

  listMachines(): Promise<Machine[]> {
    return this.authenticated('/v1/machines');
  }

  createMachine(machine: CreateMachineRequest): Promise<Machine> {
    return this.authenticated('/v1/machines', { method: 'POST', body: machine });
  }

  getMachine(id: string): Promise<Machine> {
    return this.authenticated(`/v1/machines/${encodeURIComponent(id)}`);
  }

  getTelemetry(id: string, { cursor = null, limit = 100, signal }: TelemetryQuery = {}): Promise<TelemetryPage> {
    const query = cursor === null ? `limit=${limit}` : `limit=${limit}&cursor=${encodeURIComponent(cursor)}`;
    return this.machineRead(`/v1/machines/${encodeURIComponent(id)}/telemetry?${query}`, signal);
  }

  async getBaseline(id: string, signal?: AbortSignal): Promise<MachineBaseline | null> {
    try {
      return await this.machineRead<MachineBaseline>(`/v1/machines/${encodeURIComponent(id)}/baseline`, signal);
    } catch (error) {
      if (error instanceof ApiError && error.status === 404 && error.code === 'baseline_not_found') return null;
      throw error;
    }
  }

  setBaseline(id: string, values: BaselineValues): Promise<MachineBaseline> {
    return this.authenticated(`/v1/machines/${encodeURIComponent(id)}/baseline`, { method: 'PUT', body: values });
  }

  listAlerts(id: string, signal?: AbortSignal): Promise<MachineAlert[]> {
    return this.machineRead(`/v1/machines/${encodeURIComponent(id)}/alerts`, signal);
  }

  acknowledgeAlert(id: string): Promise<void> {
    return this.authenticated(`/v1/alerts/${encodeURIComponent(id)}/acknowledge`, { method: 'POST' });
  }

  async claimDevice(claim: ClaimDeviceRequest): Promise<ClaimedDevice> {
    const result = await this.authenticated<ClaimedDevice>('/v1/devices/claim', { method: 'POST', body: claim });
    if (result?.status !== 'claimed' || result.machine_id !== claim.machine_id.toLowerCase()
      || result.device_id !== claim.device_id.toLowerCase()) {
      throw new ApiError(0, 'invalid_response', 'The server did not confirm the device claim.');
    }
    return result;
  }

  private async machineRead<T>(path: ApiPath, signal?: AbortSignal): Promise<T> {
    const generation = this.session?.getAccess()?.generation;
    if (generation === undefined) throw sessionRequired();
    if (signal?.aborted) throw cancelledRequest();

    if (this.activeMachineReads < maxMachineReads) {
      this.activeMachineReads++;
    } else {
      await new Promise<void>((start) => { this.waitingMachineReads.push(start); });
    }

    try {
      if (signal?.aborted) throw cancelledRequest();
      if (generation !== this.session?.getAccess()?.generation) throw sessionRequired();
      return await this.authenticated<T>(path, { signal });
    } finally {
      const next = this.waitingMachineReads.shift();
      if (next) next();
      else this.activeMachineReads--;
    }
  }

  async authenticated<T>(path: ApiPath, request: JsonRequest = {}): Promise<T> {
    const session = this.session;
    const grant = session?.getAccess();
    if (!session || !grant) throw sessionRequired();

    const result = await this.send<T>(path, request, grant.token).catch(async (error: unknown) => {
      if (!(error instanceof ApiError) || error.status !== 401) throw error;
      if (request.signal?.aborted) throw cancelledRequest();
      const renewed = await session.refreshAccess(grant);
      if (session.getAccess()?.generation !== grant.generation) throw sessionRequired();
      return this.send<T>(path, request, renewed.token);
    });

    if (session.getAccess()?.generation !== grant.generation) throw sessionRequired();
    return result;
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

    const signal = request.signal;
    if (signal?.aborted) throw cancelledRequest();
    const controller = new AbortController();
    const abort = () => controller.abort();
    signal?.addEventListener('abort', abort);
    const timer = setTimeout(abort, 15_000);
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
      signal?.removeEventListener('abort', abort);
    }
  }
}
