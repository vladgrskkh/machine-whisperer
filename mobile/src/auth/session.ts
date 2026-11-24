import type { AccessGrant, ApiClient } from '../api/client';
import { ApiError, sessionRequired } from '../api/errors';
import type { Credentials, TokenPair } from '../api/types';

export type SessionStatus = 'loading' | 'signedOut' | 'signedIn' | 'clearing' | 'cleanupFailed';

export type TokenStore = {
  load(): Promise<TokenPair | null>;
  save(pair: TokenPair): Promise<void>;
  clear(): Promise<void>;
};

type AuthApi = Pick<ApiClient, 'login' | 'refresh' | 'logout'>;

function storageError() {
  return new ApiError(0, 'secure_storage_error', 'Could not update the session in secure storage. Try again.');
}

export class Session {
  private status: SessionStatus = 'loading';
  private tokens: TokenPair | null = null;
  private generation = 0;
  private readonly listeners = new Set<() => void>();
  private initialization?: Promise<void>;
  private activeRefresh?: { generation: number; promise: Promise<AccessGrant> };
  private storageWrites = Promise.resolve();
  private cleanup?: Promise<void>;

  constructor(private readonly store: TokenStore, private readonly api: AuthApi) {}

  getStatus = (): SessionStatus => this.status;

  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => { this.listeners.delete(listener); };
  };

  initialize(): Promise<void> {
    this.initialization ??= this.restore();
    return this.initialization;
  }

  getAccess(): AccessGrant | null {
    return this.tokens ? { token: this.tokens.access_token, generation: this.generation } : null;
  }

  async login(credentials: Credentials): Promise<void> {
    await this.initialize();
    if (this.tokens) throw new ApiError(0, 'session_exists', 'Sign out before signing in again.');
    if (this.status !== 'signedOut') throw storageError();
    const generation = this.generation;
    const pair = await this.api.login(credentials);
    await this.save(pair, generation, true);
  }

  async logout(): Promise<void> {
    await this.initialize();
    const token = this.tokens?.refresh_token;
    const clearLocal = this.clear();
    // The API revokes the session even if an in-flight refresh has consumed this token.
    const revokeRemote = token && this.api.logout(token).catch(() => undefined);
    await Promise.all([clearLocal, revokeRemote]);
  }

  async refreshAccess(grant: AccessGrant): Promise<AccessGrant> {
    const current = this.getAccess();
    if (!current || !this.tokens || current.generation !== grant.generation) throw sessionRequired();
    if (current.token !== grant.token) return current;
    if (this.activeRefresh?.generation === grant.generation) return this.activeRefresh.promise;

    const refresh = {
      generation: grant.generation,
      promise: this.rotate(this.tokens.refresh_token, grant.generation),
    };
    this.activeRefresh = refresh;
    try {
      return await refresh.promise;
    } finally {
      if (this.activeRefresh === refresh) this.activeRefresh = undefined;
    }
  }

  private async restore() {
    this.tokens = await this.store.load().catch(() => null);
    this.updateStatus(this.tokens ? 'signedIn' : 'signedOut');
  }

  private async rotate(token: string, generation: number): Promise<AccessGrant> {
    try {
      // A restart during the request must never restore a potentially spent refresh token.
      await this.writeStorage(() => this.store.clear()).catch(() => { throw storageError(); });
      if (generation !== this.generation) throw sessionRequired();
      const pair = await this.api.refresh(token);
      await this.save(pair, generation);
      return { token: pair.access_token, generation };
    } catch (error) {
      // A lost response can still mean the server consumed this refresh token.
      if (generation === this.generation) await this.clear();
      throw error;
    }
  }

  private save(pair: TokenPair, generation: number, newSession = false) {
    return this.writeStorage(async () => {
      if (generation !== this.generation) throw sessionRequired();
      await this.store.save(pair).catch(() => { throw storageError(); });
      if (generation !== this.generation) throw sessionRequired();
      if (newSession) this.generation += 1;
      this.tokens = pair;
      this.updateStatus('signedIn');
    });
  }

  private clear() {
    if (this.cleanup) return this.cleanup;

    this.generation += 1;
    this.tokens = null;
    this.cleanup = this.writeStorage(() => this.store.clear()).then(
      () => { this.updateStatus('signedOut'); },
      () => {
        this.updateStatus('cleanupFailed');
        throw storageError();
      },
    ).finally(() => { this.cleanup = undefined; });
    this.updateStatus('clearing');
    return this.cleanup;
  }

  private writeStorage(operation: () => Promise<void>) {
    const write = this.storageWrites.then(operation);
    this.storageWrites = write.catch(() => undefined);
    return write;
  }

  private updateStatus(status: SessionStatus) {
    if (this.status === status) return;
    this.status = status;
    this.listeners.forEach((listener) => listener());
  }
}
