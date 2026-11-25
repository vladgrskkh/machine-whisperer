# Device-Scoped Telemetry, E2E Tests, and Local Load Test

**Status:** Approved
**Date:** 2026-11-25

## Intent

Add the firmware-facing device-scoped telemetry URL, verify the full user,
machine, device, and telemetry flow against the disposable Compose stack in CI,
replace the ad-hoc shell test harness with a regular test suite, and add a
local k6 load test for an already-running deployment. Reduce Testcontainers
startup time by sharing one PostgreSQL container across the API integration
package while keeping separate schemas per test.

## Current state

- The project design specifies `POST /v1/devices/me/telemetry`, but the API
  currently exposes only `POST /v1/machines/{machine_id}/telemetry`.
- Device authentication already puts the authenticated device ID and its
  claimed machine ID in the request principal. The ingestion use case already
  verifies that the requested machine matches that binding and owns the
  transaction, detector, idempotency, and outbox behavior.
- CI starts a disposable Compose stack and has separate smoke and k6 E2E shell
  scripts. The E2E seeds through real auth/enrollment APIs and sends k6
  scenarios, but the coverage is centered on successful scenarios.
- Deployment checks are mostly standalone shell tests, including shell tests
  that invoke other shell scripts. API Testcontainers tests currently start a
  fresh PostgreSQL container for each integration test, with a unique schema
  per test; a full baseline run took about 4.5 minutes at `-parallel=4`.
- A local VPS sample showed ClickHouse as the main CPU consumer: 31.6% CPU and
  984 MB RSS in the host process snapshot; the ClickHouse data directory was
  295 MB. Its system log tables recorded thousands of part creations/removals
  and merges in 24 hours, while its query log showed 48 completed queries with
  a maximum duration of 34 ms. Certbot was not running. This points to
  ClickHouse system-log background merges; this design does not change its
  logging or merge settings.

## Approaches

### Device URL

1. **Replace the existing machine-scoped route.** This leaves one API contract
   but breaks callers using the currently deployed path.
2. **Add the device-scoped route and retain the existing path temporarily
   (recommended).** The new route derives machine identity from the device
   principal; the path-based route remains for compatibility while firmware
   and all clients migrate. E2E and k6 callers use the new route.

### Deployment and E2E testing

1. **Add more Bash test wrappers.** Rejected because it expands the current
   custom shell harness.
2. **Use Python `unittest` for script contracts and Compose E2E assertions
   (recommended).** This uses the existing Python standard library to execute
   helper scripts under controlled temporary tools and to drive the full
   Compose stack through real HTTP. Operational scripts can remain Bash where
   appropriate; the tests and assertions live in Python. The full-stack suite
   consolidates the existing smoke and k6 E2E checks into one Compose lifecycle
   to avoid starting duplicate stacks.

### PostgreSQL Testcontainers lifecycle

1. **Keep one PostgreSQL container per test.** It gives strong isolation but
   the current suite takes several minutes due to repeated container startup.
2. **Share one package-scoped PostgreSQL Testcontainer (selected).** Start
   it once in the integration test package; each test keeps its own connection
   pool, unique schema, migrations, and schema cleanup. This retains test
   isolation while avoiding one container start per test.

### Load-test execution

1. **Run the real-server load test from CI using fixture secrets.** Rejected;
   production device keys should not enter CI.
2. **Run the real-server load test locally from a private fixture file
   (selected by the user).** CI runs only offline k6 validation and disposable
   Compose E2E; the user runs load against the real server locally.

## API behavior

Add `POST /v1/devices/me/telemetry` using the existing v1 body and device
bearer format. `DeviceToken` supplies the device ID and claimed machine ID;
the handler passes the bound machine to the existing ingestion use case, with
no machine ID supplied in the path or body. Preserve current 202, idempotency,
ordering, detector, outbox, auth, and sanitized error behavior. User JWTs do
not authenticate this route, and device keys remain invalid on user routes.

Keep `POST /v1/machines/{machine_id}/telemetry` temporarily for compatibility.
Update the k6 demo and Compose E2E to exercise `/v1/devices/me/telemetry`.

## E2E behavior

CI starts one random, disposable full Compose project. A Python `unittest`
suite seeds through the real signup, login, machine, baseline, provisioning,
and claim APIs, then exercises the device endpoint using the resulting
credentials. Cases include:

- Valid device submit and identical retry both return 202; telemetry history
  eventually contains the event after outbox delivery.
- Missing/invalid device credentials return the existing auth failures; a user
  token cannot submit device telemetry, and a device key cannot access user
  routes.
- The device endpoint requires no machine ID and always writes only to the
  claimed machine. Existing k6 scenarios continue to verify healthy, alert,
  recovery, and unknown detector behavior.
- Existing deployment smoke assertions for Nginx TLS/proxy restrictions and
  Postgres/ClickHouse persistence remain covered by the same disposable stack.

The Python test suite replaces the current `test-*.sh` assertion wrappers and
is invoked directly from CI. It reuses the existing seed helper's verified
signup, login, machine, baseline, provision, and claim flow, then adds explicit
HTTP E2E assertions for the device-scoped endpoint. One Compose lifecycle also
covers the current smoke assertions for TLS/proxy restrictions and Postgres/
ClickHouse persistence, avoiding two full stack startups. The suite tears down
its random Compose project, volumes, images, and temporary credentials.

## Test-container lifecycle

The API integration package starts one Testcontainers PostgreSQL instance in
`TestMain`. Each test creates a random schema, runs migrations on that schema,
uses a schema-scoped pool, and drops the schema during cleanup. Tests remain
parallel and isolated. The shared container is terminated after the package
finishes, including failure paths. The user asked to solve this optimization
in this change rather than create a separate issue.

## Local k6 load test

The load runner targets the API origin in a local private fixture file. It does
not start or seed services, and no production fixture data is checked into the
repository or included in CI.

- Recommended default duration: 5 minutes, configurable by the operator.
- Schedule 5 telemetry requests per second, using three distinct healthy
  machine/device fixtures and one stable VU per device so observations remain
  ordered. Use baseline-near RMS readings so the run does not create alerts.
- Schedule 0.5 additional requests per second in aggregate, rotating among
  public read routes: user profile, machine list, baseline, alerts, and
  telemetry history. Telemetry submissions exercise the outbox worker
  naturally; do not call `/internal/v1/telemetry-batches` directly because it
  is the worker-to-Analytics delivery endpoint.
- Report rates, HTTP failures, latency percentiles, and dropped iterations.
  Never print fixture contents, credentials, API keys, or telemetry bodies.
- The local fixture file stays mode 0600 and outside Git. An operator can copy
  the seeded fixture to a private local path when ready to run the load test.

## Non-goals

- Removing the existing machine-scoped telemetry route in this change.
- Changing the telemetry body, baseline, detector thresholds, or outbox
  semantics.
- Running production load from GitHub Actions.
- Changing ClickHouse system-log retention or merge settings; live diagnostics
  point to system-log merge activity, but tuning it needs a separate decision.
- Firmware changes or automatic baseline learning.

## Calls left to the implementer

- Use the current device principal and existing ingestion use case for the new
  route; avoid duplicating telemetry business logic.
- Choose the most readable way to consolidate the current shell-based
  deployment checks into Python `unittest`; keep helper invocations bounded
  and sanitize stdout/stderr that may contain fixture data.
- Accept a local load duration from 1 to 14 minutes, defaulting to five
  minutes. The 14-minute cap leaves margin under the 15-minute user access
  token lifetime; do not add background token refresh to the load test.
