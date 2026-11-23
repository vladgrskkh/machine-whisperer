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

Rate limiting runs before password hashing or refresh-token rotation. Configuration:

| Variable | Default | Meaning |
| --- | --- | --- |
| `DATABASE_URL` | Required | PostgreSQL connection string |
| `API_ADDRESS` | `:8080` | HTTP listen address |
| `AUTH_RATE_LIMIT` | `30` | Auth and enrollment attempts per client IP in each window |
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

`POST /v1/auth/refresh` and `POST /v1/auth/logout` accept a JSON body containing
only `refresh_token`. The token is a canonical 43-character unpadded base64url
encoding of 32 random bytes. Malformed tokens and request bodies return `400`.
Refresh returns `200` with the same four fields as login and rotates the refresh
token atomically. All refresh responses use `Cache-Control: no-store`. Unknown,
expired, revoked and reused tokens all return `401 authentication_failed`.
Reusing a consumed token revokes its session if it is still active. Rotation retains the session's
original 30-day expiry.

Serialize refresh requests in the app and replace the stored token as soon as a
refresh succeeds. Log in again if the response is lost or the commit outcome is
uncertain. Retrying the previous token after a successful rotation triggers
reuse detection and revokes the session. Store refresh tokens in OS secure
storage, send tokens over HTTPS and keep them out of URLs and logs.

Logout returns `204` with an empty body. A current or consumed token revokes
its session. Unknown and already revoked tokens also return `204`. Other
sessions stay active. An access token issued before logout or reuse revocation
remains usable until its 15-minute lifetime and 30-second skew allowance end.

PostgreSQL retains consumed token hashes through session expiry for reuse
detection. Plan for this database growth. Expired-session cleanup is a separate
maintenance task and must preserve hashes for sessions that have yet to expire.

`GET /v1/users/me` accepts one `Authorization: Bearer <access_token>` header and
returns only `id`, `email` and `created_at`. Invalid, missing, ambiguous, expired
or device credentials return `401` with `WWW-Authenticate: Bearer`; deleted users
also return `401`. JWT validation is stateless: session revocation does not
invalidate an already-issued access token before its expiry and allowed skew.
Access JWTs use Ed25519 (`EdDSA`), a 15-minute lifetime and 30 seconds of clock
skew. They require UUID `sub`, `sid`, `jti`, issuer `machine-whisperer`, audience
`machine-whisperer-api`, issuance time and expiry.

Signup, login, refresh, device provisioning and device claim share one IP request
budget per API instance. Logout, `/me` and machine routes are outside this budget.
Rate-limit responses include `Retry-After` in seconds. The shared per-email failed-login limit is
tracked in [issue #4](https://github.com/vladgrskkh/machine-whisperer/issues/4).
Additional configuration:

| Variable | Default | Meaning |
| --- | --- | --- |
| `AUTH_SIGNING_KEY_ID` | Required | Active signing key identifier |
| `AUTH_SIGNING_KEY_FILE` | Required | Ed25519 PKCS#8 PEM private-key file |
| `AUTH_VERIFY_KEYS_FILE` | Required | JSON verification-key map file |

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

Complete client secure-token storage before public deployment.

## Device enrollment

A manufacturing station calls `POST /internal/v1/device-provisioning` over HTTPS
using a user access JWT with the signed `device_provisioner` role. Restrict this
route at ingress to the manufacturing network or station gateway. Missing or
invalid JWTs return `401`. Authenticated users without the role receive `403`.

Signup creates ordinary users. A trusted operator with database administration
access can grant or revoke the role for a verified station account. The SQL
below takes that account's UUID from the `provisioner_user_id` psql variable.

```sql
INSERT INTO user_roles (user_id, role)
VALUES (:'provisioner_user_id'::uuid, 'device_provisioner')
ON CONFLICT (user_id, role) DO NOTHING;

DELETE FROM user_roles
WHERE user_id = :'provisioner_user_id'::uuid AND role = 'device_provisioner';
```

Login and refresh load current assignments from PostgreSQL. Previously issued
access tokens retain their signed roles until their 15-minute lifetime and
30-second skew allowance end.

The provisioning request needs only the bearer header. A successful response
returns `201` with `device_id`, `api_key` and the QR object `claim_payload`. The
station programs `api_key` into that unit's secure storage. Its format is
`device_id.secret`, where the secret is 32 random bytes encoded as unpadded
base64url. Print only the `claim_payload` JSON object on the QR label. Its three
fields are `v` (integer `1`), `device_id` and `claim_code`. The independent
claim code uses the same encoding and expires after `DEVICE_CLAIM_TTL`, a
positive Go duration that defaults to `24h`.

The API stores SHA-256 verifiers and sets `Cache-Control: no-store` on
provisioning and claim responses. Exclude authorization headers, claim request
bodies and provisioning responses from station and ingress logs. Keep the API
key and QR claim code out of URLs. Discard the provisioning response after
programming and labeling the unit.

The enrollment and authentication IP counters are held in each API process. Configure a shared store before running multiple replicas. Redis-backed per-email login throttling is tracked separately in issue #4.

Owner routes accept a user access JWT. `POST /v1/machines` takes `{"name":"Pump"}`
and returns `201`. Names are trimmed and limited to 100 Unicode characters.
`GET /v1/machines` lists the user's machines, and `GET /v1/machines/{id}` retrieves
one. Machine objects contain `id`, `name` and `created_at`. A lookup outside the
user's machines returns `404`.

`POST /v1/devices/claim` takes JSON with `device_id` and `claim_code` from the QR
object and the selected `machine_id`. A successful claim returns `200` with
`device_id`, `machine_id` and `status: "claimed"`. The claim binds the device
to the user's machine and consumes the code atomically. Unknown devices,
incorrect or expired codes, revoked or already-claimed devices and unavailable
machines all return `404 claim_unavailable`. Malformed requests return `400`.

## Device telemetry

A claimed sensor sends `POST /v1/machines/{machine_id}/telemetry` over HTTPS
with one `Authorization: Bearer <device_id.secret>` header and a JSON body.
Use the API key issued during provisioning. Keep it in the device's secure
storage and out of URLs and logs. Exclude authorization headers and telemetry
bodies from ingress logs. This route uses device credentials and owner routes
use user access JWTs.

```json
{
  "schema_version": 1,
  "event_id": "a5b96d2a-5cb0-4f3b-9d64-f3f53de41071",
  "observed_at": "2026-11-23T12:00:00Z",
  "operating_state": "running",
  "vibration_rms_g": 0.043,
  "dominant_frequency_hz": null,
  "current_rms_a": null
}
```

Use a UUID for `event_id`, an RFC 3339 timestamp for `observed_at` and
`running` or `unknown` for `operating_state`. Vibration RMS is required.
Frequency and current may be omitted or set to `null`. Supply finite,
non-negative numbers for all measurement values. The body limit is 8 KiB.
Unknown keys, duplicate keys and trailing JSON are rejected.

Generate one event ID per observation and reuse it with the same values on
retry. The API compares timestamps at UTC microsecond precision and queues
each accepted event once. Send new observations in nondecreasing timestamp
order for the machine. PostgreSQL supplies the receipt time. The API accepts
observations at most five minutes ahead of that time and strictly less than
30 days old.

| Status | Result |
| --- | --- |
| `202` | The event is durably queued for export or is an identical retry. The response body is empty. |
| `400` | The machine ID or request body is invalid. |
| `401` | Device credentials are missing or invalid. |
| `404` | The device is unclaimed or revoked, or the machine is unavailable to the device. |
| `409` | The event ID conflicts with a previous submission, the observation is out of order, or the timestamp is outside the acceptance window. |
| `500` | An unexpected server error occurred. |

Telemetry responses use `Cache-Control: no-store`.
