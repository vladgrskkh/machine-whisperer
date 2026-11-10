# User authentication

**Status:** Approved

## Scope and decisions

The API manages user accounts and issues JWT access tokens to the mobile app.
Include signup, login, refresh, current-session logout and current-user lookup.
Organizations, password reset and email verification are outside the MVP.
Email serves only as an unverified login identifier. Machines will belong to
individual users; their registration is a later feature.

We considered opaque access tokens and JWT access tokens. Opaque tokens simplify
immediate revocation but require a session lookup. The project owner selected
JWTs. Rotating opaque refresh tokens are proposed to renew access without
requiring the user to enter their password each time.

The settings below were approved with this spec.

## Accounts

Use email and password for signup and login. Canonicalize email by trimming
outer whitespace and lowercasing; enforce uniqueness in PostgreSQL. Do not apply
provider-specific transformations. Accept plain email addresses up to 254 bytes.
Passwords are 15–128 Unicode characters, with a 512-byte limit; preserve their
contents exactly and do not impose character-class rules.

Store salted Argon2id password hashes, never plaintext. Start with 19 MiB memory,
two iterations and one lane; benchmark before release. No email delivery is
required for this feature.

Signup returns the created user; login is a separate request. Multiple logins
create independent sessions. Invalid credentials return the same response for
unknown email and wrong password; perform dummy hash verification for an unknown
account. Duplicate signup returns a conflict; this exposes account existence.

## Tokens and sessions

- Access JWT: 15-minute lifetime, Ed25519 signature (`EdDSA`), required `sub`
  (user UUID), `sid` (session UUID), `iss`, `aud`, `iat`, `exp` and `jti` claims.
  Issuer is `machine-whisperer`; audience is `machine-whisperer-api`.
- Accept only the configured algorithm, issuer, audience and known key ID;
  reject malformed, expired or invalidly signed tokens. Allow 30 seconds of
  clock skew. Do not place email, passwords or device credentials in JWTs.
- Supply signing keys outside source control. Sign with one active key and
  support a configured verification key set for rotation; retain old public
  keys until their access tokens have expired.
- Refresh tokens: 32 random bytes, base64url encoded, returned once and stored
  as SHA-256 hashes. Each login creates a session with a fixed 30-day expiry.
  Rotation does not extend that expiry.
- Refresh consumes the token and issues a replacement atomically. Keep consumed
  token identities until session expiry to detect reuse. Reuse revokes that
  session and rejects the request. The app must serialize refresh requests;
  a lost response requires login again rather than retrying a consumed token.
- Logout revokes the session identified by its refresh token. Existing JWTs
  remain usable until expiry (including clock skew); logout does not provide
  immediate access-token revocation. Other sessions remain active.
- The mobile app stores refresh tokens in OS secure storage. Tokens travel only
  over HTTPS and must not appear in URLs or logs. Token responses use
  `Cache-Control: no-store`.

## API

| Endpoint | Request | Success |
|---|---|---|
| `POST /v1/auth/signup` | `email`, `password` | `201`: user |
| `POST /v1/auth/login` | `email`, `password` | `200`: token pair |
| `POST /v1/auth/refresh` | `refresh_token` | `200`: replacement token pair |
| `POST /v1/auth/logout` | `refresh_token` | `204`: session revoked |
| `GET /v1/users/me` | Bearer access JWT | `200`: user |

User responses contain `id`, `email` and `created_at`. Token responses contain
`access_token`, `token_type: Bearer`, `expires_in: 900` and `refresh_token`.
Invalid or expired refresh tokens return `401`; logout returns `204` for an
unknown or already revoked token. `/users/me` returns `401` if its user no longer
exists. Device API keys cannot authenticate user endpoints.

Errors use `{"error":{"code":"...","message":"..."}}`. Use `400` for invalid
input, `401` for authentication failure, `409` for duplicate signup, `429` for
rate limits and `500` for unexpected failures. Reject unknown JSON fields and
limit request bodies to 8 KiB. Do not expose database or cryptographic errors.

Protect signup, login and refresh with configurable rate limits. Proposed launch
limits are 30 requests per minute per client IP across these endpoints and five
failed login attempts per email per minute. Trust forwarded client addresses
only from the configured load balancer. A single-instance limiter is sufficient
at launch; shared limits must precede scaling auth to multiple instances.

## Storage and boundaries

PostgreSQL stores users, sessions and refresh-token hashes with uniqueness and
foreign-key constraints. Session creation and refresh rotation use transactions
controlled by use cases; concurrent consumption must not create two successors.
Transport stays in handlers, business rules in use cases, models in domain and
password hashing, signing and persistence in infrastructure. Interfaces belong
to their consumers.

## Acceptance and delivery

Integration tests use real HTTP transport and PostgreSQL. Cover signup and
concurrent duplicates, login, `/users/me`, expiry, logout, refresh rotation and
reuse, concurrent refresh, device-token rejection and request limits. Unit tests
cover token validation and password boundaries, using controlled clocks.

Keep delivery reviewable: first account storage and signup, then login and JWT
validation, then refresh/logout. Do not expose an unfinished auth flow publicly.
This spec remains separate from the persistent product-wide design.

## References

- [JWT validation guidance](https://www.rfc-editor.org/rfc/rfc8725.html).
- [Refresh-token rotation guidance](https://www.rfc-editor.org/rfc/rfc9700.html#section-4.14);
  used as security guidance, without introducing an OAuth server.
- [Password storage guidance](https://cheatsheetseries.owasp.org/cheatsheets/Password_Storage_Cheat_Sheet.html).
