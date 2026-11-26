# Machine Whisperer API

[![API coverage](https://img.shields.io/endpoint?url=https%3A%2F%2Fraw.githubusercontent.com%2Fvladgrskkh%2Fmachine-whisperer%2Fcoverage-badges%2Fapi.json)](https://github.com/vladgrskkh/machine-whisperer/actions/workflows/ci.yml)

## HTTP contract

The generated [OpenAPI 3.1 YAML](docs/openapi/openapi.yaml) and
[Swagger 2.0 YAML](docs/openapi/swagger.yaml) describe all API operations,
including restricted manufacturing and the process health probe. Both files
come from the same Go handler annotations. Fetch them from a running API at
`GET /v1/openapi.yaml` and `GET /v1/swagger.yaml`.

Open `/v1/swagger/` on the running API to browse the OpenAPI 3.1 contract and
try requests. Use **Authorize** to supply a credential for protected operations.
The UI keeps credentials in memory for the current page and disables remote
validation. The API embeds the browser assets from [Swagger UI 5.36.0](https://github.com/swagger-api/swagger-ui/releases/tag/v5.36.0)
alongside their [licenses and notices](docs/openapi/swagger/).

After changing an HTTP route or its contract, update the handler annotations and
run these commands from `api/`.

```sh
make swagger
git diff -- docs/openapi
make swagger-check
```

Commit both regenerated files with the change. CI runs `make swagger-check`
to regenerate and compare both copies. Swaggo v1 generates Swagger 2.0, and Swaggo v2 generates OpenAPI 3.1 from the
same handler comments. A pinned yq expression fixes the OpenAPI 3 schema
keywords for nullable fields and the strictly positive standard deviation.
The commands use the Go toolchain and download generation dependencies on
first use.

## Local development

The API requires Go 1.26.5, PostgreSQL and golangci-lint v2.12.2.
Install the pinned tools with `make tools`, create a PostgreSQL database and
provision the key files described below, then:

```sh
export AUTH_SIGNING_KEY_ID='key-2026-11'
export AUTH_SIGNING_KEY_FILE='/run/secrets/auth-signing.pem'
export AUTH_VERIFY_KEYS_FILE='/run/secrets/auth-public-keys.json'
export DATABASE_URL='postgres://postgres:postgres@localhost:5432/machine_whisperer?sslmode=disable'
export ANALYTICS_URL='http://localhost:8081'
export ANALYTICS_INGEST_API_KEY='<matching analytics ingest token>'
make migrate-up
make run
```

Use `sslmode=disable` only for local development. Supply production database
credentials through the deployment environment. Migrations run explicitly;
starting the server does not apply them. `make migrate-status` inspects the
migration state; `make migrate-down` rolls back the latest migration and may
remove stored data.

The API listens on `API_ADDRESS` (default `:8080`). Startup checks the database
with a 10-second deadline. A separate HTTP listener serves Prometheus at
`GET /metrics` on `API_METRICS_ADDRESS`. The default binds to loopback. On
shutdown the API drains both listeners and waits for the background workers
before closing the pool.

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
| `API_METRICS_ADDRESS` | `127.0.0.1:9091` | Private Prometheus HTTP listen address. Use a specific IP or hostname. |
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
ports. Each database integration suite lives in its own package under
`tests/integration`. It starts a disposable `postgres:18-alpine` Testcontainer
on first use and stops it in `TestMain` after the suite finishes. Each test
gets a connection pool and a freshly migrated schema. These fixtures require
Docker and use the container's URL.

`make test-integration` also runs the race detector. Lint includes
integration-tagged code. To run a focused integration test:

```sh
go test -tags=integration -race ./tests/integration/devicetelemetry -run '^TestDeviceMeTelemetry'
```

CI merges statement coverage from the unit and integration-tagged suites for
the API badge. The merge counts each statement once, including where the
suites overlap. Reports cover the API command and application packages and
exclude generated Go files, test helpers, and mocks. The run summary shows
the suite and merged totals. Download the Go profiles and HTML reports from
the run's Artifacts section.

To regenerate the hashing mock used by handler tests:

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

PostgreSQL records the authenticated JWT subject internally as the device's
`provisioned_by_user_id`. Existing enrollments can have a null value. Deleting
a provisioning account clears this association and preserves its devices.
Provisioning and claim responses omit the provisioner identity.

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
`GET /v1/machines` lists the user's active machines, and `GET /v1/machines/{id}`
retrieves one. Machine objects contain `id`, `name` and `created_at`. A lookup
outside the user's active machines returns `404`.

`DELETE /v1/machines/{id}` archives an owned machine and returns `204` with an
empty body, including on an owner's retry. Send no request body. A malformed ID
returns `400 invalid_input`, and a missing machine or one owned by another user
returns `404 machine_not_found`. The machine, baseline, alerts and telemetry
history become unavailable through owner routes.

Archiving retains the machine record, baseline, detector state, alerts,
telemetry history and existing sensor bindings. The claim code for each bound
sensor stays consumed. Valid sensor credentials receive `404 machine_not_found`
when submitting to the archived machine. The outbox continues to deliver
previously accepted telemetry under the original machine ID.

`POST /v1/devices/claim` takes JSON with `device_id` and `claim_code` from the QR
object and the selected `machine_id`. A successful claim returns `200` with
`device_id`, `machine_id` and `status: "claimed"`. The claim binds the device
to the user's machine and consumes the code atomically. Unknown devices,
incorrect or expired codes, revoked or already-claimed devices and unavailable
machines all return `404 claim_unavailable`. Malformed requests return `400`.

## Machine baselines

Machine owners use a user access JWT to read or set the current RMS vibration
baseline at `/v1/machines/{id}/baseline`. Send a `PUT` with both values in g.

```json
{"mean_rms_g":0.4,"stddev_rms_g":0.05}
```

The mean must be finite and nonnegative. The standard deviation must be finite
and positive. Both `GET` and a successful `PUT` return `200` with `machine_id`,
`mean_rms_g`, `stddev_rms_g`, `version` and `updated_at` (UTC). The first write
starts at version 1. Changing either value increments the version and updates
the timestamp. Repeating the same values preserves both.

`GET` returns `404 baseline_not_found` when there is no baseline available to the
owner. `PUT` returns `404 machine_not_found` when the machine is missing or
belongs to another user. Malformed IDs, bodies and values return `400`.

## Machine alerts

Machine owners use a user access JWT for both alert routes.

| Method and path | Success |
| --- | --- |
| `GET /v1/machines/{id}/alerts` | `200` with the machine's alerts, newest trigger time first. An empty history returns `[]`. |
| `POST /v1/alerts/{id}/acknowledge` | `204` with no body. Accepts both active and recovered alerts. |

An alert lists its ID, machine ID, trigger device and event IDs, observation
time, z-score, and the baseline version, mean and standard deviation used at
the trigger. It also reports the last and peak z-scores and the recovery
device ID, event ID, observation time and baseline version. An alert is active
while `recovered_at` is `null`. `acknowledged_at` is `null` until the first
acknowledgement. Acknowledging again preserves that timestamp. Acknowledgement
leaves an active alert open. An unknown or another user's machine or alert
returns `404`. A malformed ID returns `400`.

For each newly accepted `running` observation with a baseline, the API computes
`abs(vibration_rms_g - mean_rms_g) / stddev_rms_g`. A score at or above the alert
threshold advances the opening count when there is no active alert. A score at
or below the recovery threshold advances the recovery count when there is an
active alert. Any other score breaks that streak. `unknown` observations reset
both pending counts and leave active alerts unchanged. Changing the baseline
resets pending counts on the next accepted observation and leaves active alerts
open. Identical baseline writes and exact telemetry retries preserve detector
state. Telemetry is accepted normally before a baseline is set.

The following environment settings apply to all machines. Thresholds must be
finite, with the alert threshold greater than the nonnegative recovery
threshold. Counts must be positive integers.

| Setting | Default |
| --- | --- |
| `DETECTOR_ALERT_Z_THRESHOLD` | `3` |
| `DETECTOR_RECOVERY_Z_THRESHOLD` | `2` |
| `DETECTOR_ALERT_SUSTAINED_OBSERVATIONS` | `3` |
| `DETECTOR_RECOVERY_SUSTAINED_OBSERVATIONS` | `3` |

These defaults are starting values for field validation. Scores beyond the
finite float64 range are evaluated as above the thresholds and stored as the
maximum finite value.

## Device telemetry

A claimed sensor sends `POST /v1/devices/me/telemetry` over HTTPS with one
`Authorization: Bearer <device_id.secret>` header and a JSON body. The API
uses the machine assigned to the authenticated device. Send the observation
fields shown below. Use the API key issued during provisioning. Keep it in
the device's secure storage and out of URLs and logs. Exclude authorization
headers and telemetry bodies from ingress logs. Owner routes use user access
JWTs.

Existing clients can also send `POST /v1/machines/{machine_id}/telemetry`
with the assigned machine ID. Both routes share the same event IDs and
accept identical retries across either URL.

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
| `400` | The request body or the compatibility route's machine ID is invalid. |
| `401` | Device credentials are missing or invalid. |
| `404` | The device is unclaimed or revoked, or the machine is unavailable to the device. |
| `409` | The event ID conflicts with a previous submission, the observation is out of order, or the timestamp is outside the acceptance window. |
| `500` | An unexpected server error occurred. |

Telemetry responses use `Cache-Control: no-store`.

## Machine telemetry history

`GET /v1/machines/{machine_id}/telemetry` requires a user JWT and is available
to the machine owner. Observations appear after the outbox delivers them to
Analytics. Pages are ordered newest first by observation time, device ID and
event ID, so observations with the same timestamp paginate consistently.

Use `limit` to request 1 to 1,000 observations. The default is 100. Pass the
previous page’s `next_cursor` in the `cursor` query parameter to read older
observations. The cursor is opaque and belongs to the machine that issued it.
An exhausted page has a null `next_cursor`.

```json
{
  "items": [
    {
      "device_id": "77777777-7777-4777-8777-777777777777",
      "event_id": "88888888-8888-4888-8888-888888888888",
      "received_at": "2026-11-24T12:01:00.987654Z",
      "schema_version": 1,
      "observed_at": "2026-11-24T12:00:00.123456Z",
      "operating_state": "running",
      "vibration_rms_g": 0.043,
      "dominant_frequency_hz": null,
      "current_rms_a": null
    }
  ],
  "next_cursor": null
}
```

| Status | Meaning |
| --- | --- |
| `200` | The requested page. An empty page uses `"items":[]`. |
| `400` | The machine ID, limit or cursor is invalid. |
| `401` | User credentials are missing or invalid. |
| `404` | The machine is missing, archived or belongs to another user. |
| `503` | The PostgreSQL ownership lookup failed, or Analytics could not serve history due to a transport, storage, or service error. Retry the request later. |

History responses use `Cache-Control: no-store`. The API uses
`ANALYTICS_URL` and `ANALYTICS_INGEST_API_KEY` for history requests. These
requests have a 10-second timeout and reject redirects.

## Telemetry export

The API process runs an outbox worker alongside HTTP. It sends pending events
to the [Analytics service](../analytics/README.md), retries failed deliveries
and periodically prunes delivered rows. Configure the same ingest token in both
services and use HTTPS for traffic between hosts.

| Variable | Default | Meaning |
| --- | --- | --- |
| `ANALYTICS_URL` | Required | Analytics HTTP or HTTPS base URL |
| `ANALYTICS_INGEST_API_KEY` | Required | Dedicated token shared with Analytics |
| `OUTBOX_HTTP_TIMEOUT` | `30s` | Analytics request timeout, shorter than the lease |
| `OUTBOX_BATCH_SIZE` | `1000` | Maximum events per send and delivered rows per prune |
| `OUTBOX_POLL_INTERVAL` | `1s` | Wait after a partial or failed batch and interval between prune attempts |
| `OUTBOX_LEASE` | `2m` | Time before another worker can reclaim an unfinished batch |
| `OUTBOX_RETRY_BASE` | `1s` | Initial retry delay |
| `OUTBOX_RETRY_MAX` | `5m` | Maximum retry delay |
| `OUTBOX_RETENTION` | `720h` | How long PostgreSQL keeps delivered rows |
