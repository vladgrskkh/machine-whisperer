# Machine Whisperer API

The API requires Go 1.26.5, PostgreSQL and golangci-lint v2.12.2.
Install the pinned tools with `make tools`, create a PostgreSQL database and
provision the key files described below, then:

```sh
export AUTH_SIGNING_KEY_ID='key-2026-11'
export AUTH_SIGNING_KEY_FILE='/run/secrets/auth-signing.pem'
export AUTH_VERIFY_KEYS_FILE='/run/secrets/auth-public-keys.json'
export DATABASE_URL='postgres://postgres:postgres@localhost:5432/machine_whisperer?sslmode=disable'
make migrate-up
make run
```

Use `sslmode=disable` only for local development. Supply production database
credentials through the deployment environment. Migrations run explicitly;
starting the server does not apply them. `make migrate-status` inspects the
migration state; `make migrate-down` rolls back the latest migration and may
remove stored data.

The API listens on `API_ADDRESS` (default `:8080`). Startup checks the database
with a 10-second deadline; cancellation drains HTTP requests before closing the
pool.

`POST /v1/auth/signup` accepts JSON `email` and `password`. It returns `201` with
`id`, canonical `email` and `created_at`; it does not create a login session.
Email is trimmed and lowercased. Passwords retain their exact contents and must
contain 15–128 Unicode characters, at most 512 bytes. Bodies are limited to 8 KiB;
unknown fields, malformed bodies and trailing JSON return `400`. Duplicate email
returns `409`, rate limits `429`, and unexpected failures `500`. Errors use
`{"error":{"code":"...","message":"..."}}`.

Rate limiting runs before password hashing. Configuration:

| Variable | Default | Meaning |
| --- | --- | --- |
| `DATABASE_URL` | Required | PostgreSQL connection string |
| `API_ADDRESS` | `:8080` | HTTP listen address |
| `AUTH_RATE_LIMIT` | `30` | Attempts per client IP in each window |
| `AUTH_RATE_LIMIT_WINDOW` | `1m` | Window duration, using Go duration syntax |
| `AUTH_RATE_LIMIT_MAX_CLIENTS` | `10000` | Maximum active client windows |
| `TRUSTED_PROXY_CIDRS` | Empty | Comma-separated proxy network CIDRs |

Forwarded headers are ignored by default. When the direct peer matches a trusted
proxy CIDR, `X-Forwarded-For` is read from right to left, skipping trusted proxy
hops and using the first untrusted address. The load balancer must overwrite the
header or append the actual connecting address; configure only its network as
trusted. Caller-supplied prefixes before the first untrusted hop are ignored
regardless of their length. Malformed trusted suffixes fall back to the direct
peer. Other forwarded headers
are ignored. Full limiter capacity rejects new clients with `429` until windows
expire, without evicting active limits. Limits are in memory per API instance;
shared limits are required before running multiple auth instances.

```sh
make build
make test
make test-integration
make lint
```

Unit tests use real HTTP connections and require permission to bind loopback
ports. Integration tests use Testcontainers to start `postgres:18-alpine`, apply
migrations and isolate each test in its own schema. They require a running Docker
engine; missing Docker fails the tests. They do not use `DATABASE_URL` or a shared
test database. `make test-integration` also runs the race detector. Lint includes
integration-tagged code. To regenerate the hashing mock used by handler tests:

```sh
mockery --name PasswordHasher --dir internal/usecase/auth --output internal/handler/auth/authmock --outpkg authmock --filename password_hasher.go --with-expecter
```

`POST /v1/auth/login` accepts the same strict email/password JSON as signup.
Success returns `200` with `access_token`, `token_type: "Bearer"`,
`expires_in: 900` and `refresh_token`. Unknown email and wrong password both
return `401 authentication_failed`. Unknown accounts perform the same Argon2id
verification against a dummy hash generated once during startup. Passwords are
never trimmed or normalized. Every login creates an independent session with a
fixed 30-day expiry and stores only the SHA-256 hash of the initial refresh
secret. Login responses, including errors and limits, use `Cache-Control: no-store`.

`GET /v1/users/me` accepts one `Authorization: Bearer <access_token>` header and
returns only `id`, `email` and `created_at`. Invalid, missing, ambiguous, expired
or device credentials return `401` with `WWW-Authenticate: Bearer`; deleted users
also return `401`. JWT validation is stateless: session revocation does not
invalidate an already-issued access token before its expiry and allowed skew.
Access JWTs use Ed25519 (`EdDSA`), a 15-minute lifetime and 30 seconds of clock
skew. They require UUID `sub`, `sid`, `jti`, issuer `machine-whisperer`, audience
`machine-whisperer-api`, issuance time and expiry. `/me` is outside the signup/login
IP budget.

Signup and login share one IP request budget. Login also allows five failed
attempts per canonical email per minute, counting in-flight attempts to prevent
concurrent bypass. Successful logins do not erase earlier failures. Bounded email
state rejects new identities when full until windows expire. Rate-limit responses
include `Retry-After` in seconds. Additional configuration:

| Variable | Default | Meaning |
| --- | --- | --- |
| `AUTH_SIGNING_KEY_ID` | Required | Active signing key identifier |
| `AUTH_SIGNING_KEY_FILE` | Required | Ed25519 PKCS#8 PEM private-key file |
| `AUTH_VERIFY_KEYS_FILE` | Required | JSON verification-key map file |
| `AUTH_EMAIL_RATE_LIMIT` | `5` | Failed/in-flight logins per canonical email |
| `AUTH_EMAIL_RATE_LIMIT_WINDOW` | `1m` | Email window duration |
| `AUTH_EMAIL_RATE_LIMIT_MAX_CLIENTS` | `10000` | Maximum active email windows |

Provision the signing key outside source control and mount it read-only for the
API process. Restrict private-key access to that process. The PEM block must have
`PRIVATE KEY` type and contain an Ed25519 PKCS#8 key. The verification file is a
JSON object mapping key IDs to standard padded base64 encodings of the raw
32-byte Ed25519 public keys, for example:

```json
{"key-2026-11":"<base64-public-key>","key-2026-10":"<previous-base64-public-key>"}
```

The placeholders are format examples and must be replaced with public keys.
The active ID must exist in the verification file and match the private key.
Startup fails with a sanitized error if key loading or dummy-hash generation
fails; the server never generates production keys or silently disables auth.
Set all three key environment variables before `make run`. To rotate, distribute
the new public key alongside the old public key to every verifier, then switch
the active signing ID and private file. Restart instances to load changed files.
Retain old public keys until all their access tokens have expired, including the
30-second skew allowance. Token-supplied paths or URLs never select keys.

Refresh and logout endpoints are intentionally reserved for the next PR. Initial
refresh tokens are persisted now but cannot yet be renewed or revoked through
HTTP. Keep this staged auth flow off public deployment until refresh/logout and
client secure-token storage are complete. Use HTTPS and never put tokens in URLs
or logs.
