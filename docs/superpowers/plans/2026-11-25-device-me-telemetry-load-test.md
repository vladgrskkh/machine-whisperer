# Device Telemetry and Deployment Tests Implementation Plan

Use Superpowers subagent driven development with an independent review for each task.

**Goal** Deliver device scoped ingest, faster PostgreSQL integration tests, one disposable Compose E2E suite, and a local k6 load profile.

**Design** See [the approved design](../specs/2026-11-25-device-me-telemetry-load-test-design.md).

## 1. Device scoped ingest

- [x] Add a Testcontainers HTTP regression for the new route covering authentication, binding, request validation and idempotency across both routes.
- [x] Register POST /v1/devices/me/telemetry with the existing device middleware. Resolve its machine from the authenticated principal and run the same ingest code as the compatibility route. Preserve the existing owner history route.
- [x] Switch demo k6 submissions to the new URL. Update the API README and run focused integration checks.

## 2. Shared PostgreSQL test container

- [x] Use package TestMain to manage one Testcontainer in api/tests/integration. Keep a separate pool and migrated schema per test and clean them up before termination.
- [x] Verify schema isolation, go test selection and failure cleanup. Run the full race enabled integration suite and compare elapsed time with the 272.6 second baseline.

## 3. Deployment tests and Compose E2E

- [x] Move deployment helper checks into Python unittest with bounded subprocess calls. Keep checks for migration, ClickHouse bootstrap, TLS helpers, Compose settings, publication policy, restore, seed and k6 archive.
- [x] Consolidate smoke and k6 E2E into one disposable Compose lifecycle. Preserve TLS and ingress restrictions, migration/health assertions, restart persistence and k6 alert cases. Add explicit HTTP tests for owner identity and isolation, enrollment and claim, device auth, successful submission, identical retry and eventual history.
- [x] Run both Python suites and make CI use their entrypoints. Document local invocation.

## 4. Local k6 load profile

- [x] Add a local launcher with a private fixture file, a 1 to 14 minute duration and a five minute default.
- [x] Schedule dedicated workers for three healthy devices with one in flight per device and five total telemetry starts per second. Schedule one owner read every two seconds, rotating through profile, machines, baseline, alerts and history. Fail failed/dropped work and report latency and rates.
- [x] Check the script offline and validate rates and routing against a disposable local target. Document local operation and keep real fixture files and real-server load out of CI.

## Finish

- [x] Resolve independent reviews and verify the combined changes.
- [ ] Open a PR.
