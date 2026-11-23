# ClickHouse Outbox Exporter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Implement and independently review each task before the next; finish with whole-branch review. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reliably deliver accepted telemetry from the API outbox to ClickHouse through a separately deployable analytics service.

**Architecture:** A relay worker in the API module leases batches from the API-owned PostgreSQL outbox and sends them to an authenticated internal analytics endpoint. Analytics validates and bulk-inserts each batch into ClickHouse, acknowledging only after ClickHouse succeeds. The API relay then marks the rows delivered. Analytics has no PostgreSQL connection.

**Tech Stack:** Go 1.26.5, pgx, chi, `clickhouse-go/v2` v2.51.0, Goose for PostgreSQL migrations, ClickHouse SQL migrations and PostgreSQL/ClickHouse Testcontainers.

**Spec:** `docs/superpowers/specs/2026-11-23-clickhouse-outbox-exporter-design.md`

## Global Constraints

- The API relay claims outbox rows with bounded `FOR UPDATE SKIP LOCKED` leases and sends batches to `POST /internal/v1/telemetry-batches`.
- The relay authenticates to Analytics with `ANALYTICS_INGEST_API_KEY`; Analytics validates the key in constant time and does not connect to PostgreSQL.
- Analytics returns success only after a complete ClickHouse batch insert. The relay marks rows delivered only after success and retries all failures with capped exponential backoff.
- `available_at` is the unified claim/retry time: when leased, set it to `lease_until`; when rescheduled, set it to the retry time. Claim due undelivered rows in `available_at` order using one partial index.
- Outbox delivery is at least once. ClickHouse uses `ReplacingMergeTree(received_at)`, partitioned by `toYYYYMM(observed_at)` and ordered by `(machine_id, observed_at, device_id, event_id)`; immediate logical dedupe requires `FINAL` or equivalent key aggregation.
- Default relay batch size is 1,000 rows, poll interval 1s, lease 2m, retry backoff 1s to 5m, and delivered-row retention 30d. These settings are configurable and validated.
- Never log database/ClickHouse URLs, service keys, outbox payloads or vibration measurements.

## Review Focus

1. A relay crash before the Analytics call leaves outbox rows pending; a crash after ClickHouse succeeds but before the relay receives the response retries safely.
2. Concurrent relay instances do not own the same unexpired lease; expired leases can be reclaimed.
3. A stale lease token cannot acknowledge or reschedule a newer worker's claim.
4. Analytics rejects missing/wrong service credentials and malformed/oversized batch payloads without leaking secrets.
5. Repeated ClickHouse batches produce one logical row per event when queried with `FINAL`.

---

### Task 1: API outbox lease schema and relay worker

**Files:**
- Create: `api/migrations/202611230004_telemetry_outbox_leases.sql`
- Create: `api/internal/domain/telemetry/outbox.go`
- Create: `api/internal/infrastructure/telemetry/postgres/outbox.go`
- Create: `api/internal/infrastructure/telemetry/analytics/http_client.go`
- Create: `api/internal/usecase/telemetry/export.go`
- Create: `api/internal/exporter/config.go`
- Create: `api/cmd/outbox-relay/main.go`
- Test: `api/tests/integration/outbox_export_test.go`

**Interfaces:**
- `telemetry.EventKey{DeviceID, EventID string}` identifies one leased row.
- `telemetry.OutboxEvent{DeviceID, EventID, MachineID string; ReceivedAt time.Time; Payload json.RawMessage}` is serialized with fields `device_id`, `event_id`, `machine_id`, `received_at` and nested `payload`; `received_at` comes from outbox `created_at`.
- `telemetry.BatchSender.Send(ctx context.Context, events []telemetry.OutboxEvent) error` makes one authenticated internal batch request.
- `telemetry.OutboxRepository.ClaimBatch(ctx context.Context, batchSize int, lease time.Duration) (leaseToken string, events []telemetry.OutboxEvent, err error)` uses database time and a bounded `SKIP LOCKED` transaction.
- `telemetry.OutboxRepository.MarkDelivered(ctx context.Context, leaseToken string, keys []telemetry.EventKey) (updated int64, err error)` only marks rows still owned by the token.
- `telemetry.OutboxRepository.Reschedule(ctx context.Context, leaseToken string, keys []telemetry.EventKey, backoff time.Duration, errorCategory string) error` releases rows for retry.
- `telemetry.OutboxRepository.DeleteDeliveredBefore(ctx context.Context, retention time.Duration, limit int) (deleted int64, err error)` prunes delivered rows in a bounded batch.
- `telemetry.ExportWorker.ProcessOnce(ctx context.Context) (claimed int, err error)` claims, sends, acknowledges or reschedules one batch; `Run(ctx)` repeats until cancellation.

- [x] Add failing Testcontainers tests named `TestOutboxLeaseClaimsRowsOnce`, `TestOutboxLeaseCanBeReclaimed`, `TestOutboxStaleLeaseCannotAcknowledge`, `TestOutboxRetryBackoff`, `TestOutboxDeliveredRetentionIsBounded` and `TestOutboxRelayRetriesAnalyticsFailure`.
- [x] Run `cd api && go test -tags=integration ./tests/integration -run '^TestOutbox' -count=1 -parallel=1` and confirm failures are due to missing lease schema/worker behavior.
- [x] Add the reversible PostgreSQL migration for `available_at`, `lease_token`, `lease_until`, `attempt_count`, `last_error_category` and a due index on `(available_at,created_at,device_id,event_id)` for all undelivered rows.
- [x] Implement the PostgreSQL adapter with a bounded ordered `FOR UPDATE SKIP LOCKED` claim; hold the transaction only for claim/ack/retry, never for the HTTP request.
- [x] Implement the Analytics HTTP client, relay worker and separate `cmd/outbox-relay`. Config includes `DATABASE_URL`, `ANALYTICS_URL`, `ANALYTICS_INGEST_API_KEY` and validated `OUTBOX_*` settings.
- [x] Mark delivered only after an HTTP success and matching lease token. On failure, release the lease, set the next attempt with capped exponential backoff and store only a sanitized error category.
- [x] Rerun focused outbox Testcontainers tests, unit tests, build, pinned lint and diff check; commit `feat: relay telemetry outbox batches` and obtain a task review.

### Task 1 review fix round 1

- [x] Use `available_at` as the unified eligibility time and set it to the lease expiry while rows are claimed or the retry time after failure.
- [x] Add one ordered partial index covering all undelivered rows; verify both custom and generic plans against 200,000 mixed outbox rows.
- [x] Keep lease-token checks for acknowledgement/retry; rerun focused and broader Testcontainers tests, unit tests, build and lint; obtain a scoped re-review.

### Task 2: Analytics batch endpoint and ClickHouse storage

**Files:**
- Create: `analytics/go.mod`
- Create: `analytics/Makefile`
- Create: `analytics/cmd/analytics/main.go`
- Create: `analytics/cmd/migrate/main.go`
- Create: `analytics/internal/config/config.go`
- Create: `analytics/internal/domain/event.go`
- Create: `analytics/internal/handler/auth.go`
- Create: `analytics/internal/handler/batch.go`
- Create: `analytics/internal/infrastructure/clickhouse/client.go`
- Create: `analytics/migrations/001_create_telemetry_events.sql`
- Test: `analytics/internal/handler/batch_test.go`
- Test: `analytics/tests/integration/clickhouse_batch_test.go`
- Modify: `.github/workflows/ci.yml`
- Modify: `docs/machine-whisperer-project-design.md`
- Modify: root `README.md` and create `analytics/README.md`

**Interfaces:**
- `analytics.Event` decodes the API relay envelope fields `device_id`, `event_id`, `machine_id`, `received_at` and nested `payload` into typed fields.
- `POST /internal/v1/telemetry-batches` accepts `{ "events": [...] }` with one `Authorization: Bearer <ANALYTICS_INGEST_API_KEY>` header.
- Enforce a maximum of 1,000 events and a 16 MiB request body; validate all entries before inserting the batch.
- `handler.BatchWriter.WriteBatch(ctx context.Context, events []domain.Event) error` performs one ClickHouse batch insert. The endpoint returns `204` only after successful insert; failures return sanitized `5xx` so the API relay retries.

- [x] Add failing handler tests for missing/wrong service key, valid batch, malformed/oversized batch, invalid fields, JSON-escaped `Z` timestamp acceptance, malformed timezone offset rejection and sanitized ClickHouse errors.
- [x] Run `cd analytics && go test ./internal/handler -count=1` after creating the module and test, and confirm it fails because the batch handler/writer is absent.
- [x] Create the explicit ClickHouse migration for the v1 event schema using `ReplacingMergeTree(received_at)`, monthly observation partitions and the specified sorting key; do not apply DDL automatically at service startup.
- [x] Implement constant-time service-key verification, strict batch decoding and the ClickHouse bulk writer with `clickhouse-go/v2` v2.51.0.
- [x] Add Testcontainers integration tests that insert a batch twice and verify `SELECT ... FINAL` returns one logical row per event; cover nullable measures, UTC timestamps and a ClickHouse insert failure returning a retryable non-2xx response.
- [x] Add analytics build/test/integration/lint targets and CI. Document migration, credentials, API relay URL and dedupe/query behavior.
- [x] Rerun analytics unit and ClickHouse Testcontainers tests, build, pinned lint and diff check; commit `feat: accept telemetry batches in analytics` and obtain a task review.

### Task 2 review fix round 1

- [x] Decode both `received_at` and `observed_at` as JSON strings before parsing; accept valid escapes and reject invalid offset hours/minutes.
- [x] Add HTTP regressions for escaped UTC markers and malformed offsets; rerun handler, ClickHouse Testcontainers, unit, build and lint checks; obtain a scoped re-review.
