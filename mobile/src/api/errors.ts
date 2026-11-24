export class ApiError extends Error {
  constructor(readonly status: number, readonly code: string, message: string) {
    super(message);
    this.name = 'ApiError';
  }
}

export function decodeApiError(status: number, body: unknown): ApiError {
  if (typeof body === 'object' && body !== null && 'error' in body) {
    const { error } = body;
    if (typeof error === 'object' && error !== null && 'code' in error && 'message' in error
      && typeof error.code === 'string' && typeof error.message === 'string') {
      return new ApiError(status, error.code, error.message);
    }
  }
  return new ApiError(status, 'http_error', 'The server could not complete the request.');
}

export function sessionRequired() {
  return new ApiError(401, 'session_required', 'Sign in to continue.');
}

export function errorMessage(error: unknown, fallback: string) {
  return error instanceof ApiError ? error.message : fallback;
}
