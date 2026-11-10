# Machine Whisperer API

The API requires Go 1.26.5, PostgreSQL and golangci-lint v2.12.2.
Install the pinned tools with `make tools`, create a PostgreSQL database, then:

```sh
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
