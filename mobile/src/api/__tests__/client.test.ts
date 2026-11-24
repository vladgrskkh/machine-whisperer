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
