export type Credentials = {
  email: string;
  password: string;
};

export type User = {
  id: string;
  email: string;
  created_at: string;
};

export type TokenPair = {
  access_token: string;
  refresh_token: string;
  token_type: 'Bearer';
  expires_in: number;
};

export function isTokenPair(value: unknown): value is TokenPair {
  if (typeof value !== 'object' || value === null) return false;
  const pair = value as Partial<TokenPair>;
  return typeof pair.access_token === 'string' && pair.access_token.length > 0
    && typeof pair.refresh_token === 'string' && pair.refresh_token.length > 0
    && pair.token_type === 'Bearer'
    && typeof pair.expires_in === 'number' && Number.isFinite(pair.expires_in) && pair.expires_in > 0;
}
