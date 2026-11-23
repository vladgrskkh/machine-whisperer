# Device Telemetry Ingestion Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Implement and independently review each task before the next; finish with whole-branch review. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Accept versioned telemetry from authenticated, claimed devices and durably queue each new event for later export.

**Architecture:** Device credentials are verified against PostgreSQL before the handler accepts telemetry for the device's assigned machine. One transaction serializes event ordering per machine, stores the event and idempotency hash, updates the machine cursor, and inserts an outbox record. Detection and ClickHouse export remain separate features.

**Tech Stack:** Go 1.26.5, chi, pgx, Goose, slog, testify and PostgreSQL Testcontainers.

**Spec:** `docs/superpowers/specs/2026-11-23-device-telemetry-design.md`

## Global Constraints

- The device sends `Authorization: Bearer device_id.secret`; the secret is 32 random bytes encoded as unpadded base64url.
- Verify the secret against the stored SHA-256 verifier using constant-time comparison. Never log the credential or request body.
- Device keys never authenticate user routes. A device may write only for its claimed, active machine.
- Telemetry v1 has exactly `schema_version`, `event_id`, `observed_at`, `operating_state`, `vibration_rms_g`, `dominant_frequency_hz` and `current_rms_a`; operating states are `running` and `unknown`.
- Normalize timestamps to UTC and PostgreSQL microsecond precision before comparison, hashing, ordering or persistence.
- Event ID scope is `(device_id, event_id)`. Identical normalized retries create no second event/outbox item; conflicting reuse returns 409.
- New events preserve nondecreasing observation order per machine. Equal timestamps are allowed. Use PostgreSQL `clock_timestamp()` after locking the machine row for receipt and time-window checks.
- Reject future observations beyond five minutes and observations 30 days old or older. Retain event identity through `observed_at + 30 days`.
- Request bodies are strict JSON bounded to 8 KiB; reject duplicate/unknown keys and trailing data.
- Accepted events and outbox entries commit atomically in PostgreSQL. No synchronous ClickHouse write or detector update occurs in this task.

## Review Focus

1. A valid device credential cannot write for another machine; test both unclaimed/revoked devices and machine mismatch.
2. Concurrent requests for one machine cannot move its observation cursor backwards or create duplicate outbox records.
3. Retries compare normalized payload values, including timestamps, optional nulls and numeric representations.
4. Outbox failure rolls back the event and machine cursor in the same transaction.
5. Logs and errors never expose device keys, authorization headers or telemetry bodies.

---

### Task 1: Telemetry model, schema and PostgreSQL storage

**Files:**
- Create: `internal/domain/telemetry/event.go`
- Modify: `internal/domain/machine/machine.go`
- Create: `migrations/202611230003_create_telemetry.sql`
- Create: `internal/infrastructure/telemetry/postgres/repository.go`
- Test: `tests/integration/telemetry_storage_test.go`
- Modify: `tests/integration/device_enrollment_test.go` to keep its migration reapply test anchored to the enrollment migration after telemetry becomes the latest migration

**Interfaces:**
- Produces `telemetry.Observation{SchemaVersion int, EventID string, ObservedAt time.Time, OperatingState string, VibrationRMSG float64, DominantFrequencyHz, CurrentRMSA *float64}` and `telemetry.Event{Observation, DeviceID, MachineID string, ReceivedAt time.Time, PayloadHash [32]byte}`.
- Produces domain sentinel `telemetry.ErrEventConflict` for unique event/outbox key conflicts; the PostgreSQL adapter wraps driver violations with this sentinel.
- Adds nullable `machine.Machine.LastTelemetryObservedAt *time.Time` as the durable per-machine order cursor.
- PostgreSQL adapter supports `LockMachineForTelemetry(ctx, machineID) (lastObservedAt *time.Time, dbNow time.Time, err error)`, event lookup including expiry, scoped single-key expiry removal, bounded machine-scoped expiry cleanup (100 identities per request), event/outbox insert and cursor update, all using the transaction from context.

The migration adds `machines.last_telemetry_observed_at`; `telemetry_events` stores `(device_id,event_id)`, machine ID, observation time, payload hash and `expires_at`, with an index on `(machine_id,expires_at)` for bounded cleanup; `telemetry_outbox` stores the complete v1 JSON payload, device/event IDs, created time and nullable delivered time. The outbox has its own unique `(device_id,event_id)` key and no foreign key to the expiring idempotency table, so unexported events survive dedupe cleanup. An outbox-key conflict maps to 409 instead of creating a second export item.

- [x] Write Testcontainers tests named `TestTelemetryStorageConstraints`, `TestTelemetryEventAndOutboxInsertAtomically`, `TestTelemetryMachineCursor` and `TestTelemetryExpiredIdempotencyCleanup` for unique keys, payload hash length, machine/device foreign keys, cursor state, independent outbox retention and 30-day idempotency expiry.
- [x] Run `go test -tags=integration ./tests/integration -run '^TestTelemetry' -count=1` and confirm the new tests fail against the absent schema.
- [x] Implement the reversible migration: add `machines.last_telemetry_observed_at`; add telemetry idempotency/event records with normalized fields and payload hash; add a durable outbox payload row per accepted event.
- [x] Implement the PostgreSQL adapter using the transaction from context. Make outbox insertion and event/cursor writes participate in the caller's transaction.
- [x] Rerun `go test -tags=integration ./tests/integration -run '^TestTelemetry' -count=1` and lint; confirm storage tests pass.
- [x] Commit `feat: add telemetry event storage` and obtain a task review before Task 2.

### Task 2: Device authentication and telemetry ingestion HTTP flow

**Files:**
- Create: `internal/usecase/device/authenticate.go`
- Modify: `internal/infrastructure/device/postgres/repository.go`
- Create: `internal/handler/middleware/device_token.go`
- Create: `internal/usecase/telemetry/ingest.go`
- Create: `internal/handler/telemetry/http.go`
- Modify: `internal/application/application.go`
- Modify: `internal/application/http.go`
- Modify: `README.md`
- Test: `tests/integration/device_telemetry_test.go`

**Interfaces:**
- `device.Authenticator.Authenticate(ctx context.Context, deviceID string, secret device.Secret) (machineID string, err error)` returns no machine for an unclaimed or revoked device and maps invalid credentials to one domain error.
- `middleware.DeviceToken` accepts a small authenticator interface, parses the bearer credential strictly, and stores `{deviceID,machineID}` in request context.
- `telemetry.Ingest.Ingest(ctx context.Context, deviceID, assignedMachineID, requestedMachineID string, observation telemetry.Observation) error` owns the transaction boundary and uses small repository/transaction interfaces defined at the use case.
- `telemetry.NewIngest` handler parses the path UUID and strict bounded JSON; the use case rejects mismatched device/machine binding.
- `device.Repository.FindForAuthentication(ctx, deviceID string) (device.Device, error)` supplies the key verifier, claim state and revocation state without exposing verifier values beyond the auth use case.
- The ingestion use case calls `LockMachineForTelemetry`, `DeleteExpiredTelemetryEvents`, `FindTelemetryEvent`, `InsertTelemetryEvent`, `InsertTelemetryOutbox` and `UpdateMachineTelemetryCursor` inside one `TransactionManager.WithinTransaction` call.

- [x] Add failing real-HTTP/Testcontainers tests `TestDeviceTelemetryRejectsInvalidCredentials`, `TestDeviceTelemetryRejectsUnclaimedRevokedAndWrongMachine`, `TestDeviceTelemetryRejectsRevocationBetweenAuthenticationAndIngest`, and `TestDeviceKeyCannotAuthenticateUserRoutes`.
- [x] Add failing tests `TestDeviceTelemetryValidatesV1Payload`, `TestDeviceTelemetryIdenticalRetryIsIdempotent`, `TestDeviceTelemetryConflictingEventIDReturnsConflict`, `TestDeviceTelemetryOutboxKeyConflictReturnsConflict`, `TestDeviceTelemetryRejectsFutureStaleAndOutOfOrderEvents`, `TestDeviceTelemetryRejectsFutureUTCYearOverflow`, `TestDeviceTelemetryConcurrentEventsPreserveOrder`, `TestDeviceTelemetryOutboxFailureRollsBack`, and `TestDeviceTelemetryLogsExcludeSecretsAndBody`.
- [x] Run `go test -tags=integration ./tests/integration -run '^TestDeviceTelemetry|^TestDeviceKeyCannot' -count=1` and confirm the new tests fail because the device telemetry route and authentication are not implemented.
- [x] Implement `device.Authenticator.Authenticate(ctx, deviceID, secret) (machineID, error)`: look up active device state, verify the 32-byte secret in constant time and return the assigned machine ID. Implement `middleware.DeviceToken` to parse one `device_id.secret` bearer value and carry only device/machine IDs in context.
- [x] Implement `telemetry.Ingest.Ingest(ctx, deviceID, assignedMachineID, requestedMachineID, observation) error`: lock the device then machine rows, sample PostgreSQL time after locking, enforce the 5-minute/30-day bounds and observation order, normalize and hash the event, make duplicate behavior idempotent, then insert event/outbox and update the cursor atomically.
- [x] Add `POST /v1/machines/{machine_id}/telemetry` with `middleware.NoStore`, ensuring user auth and device auth remain separate. Return 202 for accepted/identical retries; return the documented sanitized 400/401/404/409/500 errors.
- [x] Document device authentication and the telemetry request/response behavior without exposing secrets.
- [x] Rerun `go test -tags=integration ./tests/integration -run '^TestDeviceTelemetry|^TestDeviceKeyCannot' -count=1` and confirm all new HTTP cases pass. Then run the full unit suite, full `make test-integration`, `make build`, pinned lint and `git diff --check`.
- [x] Commit `feat: accept authenticated device telemetry`; obtain task and whole-branch reviews before opening a PR.

### Final review fix wave

- [x] Add real-HTTP/Testcontainers coverage `TestDeviceTelemetryExpiryCleanupIsMachineScopedAndBounded` and `TestDeviceTelemetryEscapedObservedAtIsAccepted`, plus a storage lock-wait test proving `LockMachineForTelemetry` samples database time after obtaining the lock.
- [x] Reproduce the cross-machine cleanup, escaped-string timestamp and post-lock-clock gaps before fixing them.
- [x] Scope idempotency cleanup to the locked machine, cap each batch at 100 rows, return/handle the submitted identity's `expires_at` explicitly, and decode `observed_at` as a JSON string before RFC3339 parsing.
- [x] Run the focused regressions, full serial Testcontainers suite, unit tests, build and lint; obtain a scoped re-review.
