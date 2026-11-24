# Telemetry History API Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Implement and independently review each task before the next; finish with whole-branch review. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Serve authorized, bounded historical telemetry pages to the app through the API while keeping ClickHouse access in Analytics.

**Architecture:** Analytics exposes an authenticated internal query route backed by a ClickHouse repository using `FINAL`. The API verifies machine ownership, then proxies the query and next cursor. The API has no ClickHouse credentials, and Analytics has no API PostgreSQL connection.

**Tech Stack:** Go 1.26.5, chi, pgx, ClickHouse Go client v2.51.0, Testcontainers, testify, mockery.

**Spec:** `docs/superpowers/specs/2026-11-24-telemetry-history-api-design.md`

## Global Constraints

- Keep user JWT and machine ownership checks in the API.
- Keep ClickHouse queries and the internal service credential in Analytics; do not add API ClickHouse credentials or shared PostgreSQL access.
- Return at most 1,000 raw telemetry observations per page, newest first.
- Use the complete stable key `(observed_at, device_id, event_id)` for cursor order and filter.
- Preserve `DateTime64(6)` timestamp precision and compare cursor device/event IDs as native UUIDs.
- Query with `FINAL` so at-least-once export retries do not appear as duplicate observations.
- Return only public telemetry v1 fields; do not expose ClickHouse types, SQL errors or credentials.
- Reject limits outside `1..1,000`, malformed cursors and cross-machine cursors with `400`.
- Map internal Analytics `400` to a public invalid-query response; map downstream auth/transport/timeout/storage errors to sanitized `503`.

## Review Focus

1. Users cannot read history for a machine they do not own, even by guessing IDs or cursors.
2. Equal observation timestamps across devices paginate without repeats or gaps.
3. Retries appear once in history because the query uses `FINAL`.
4. Missing, malformed, oversized or cross-machine cursors/limits are rejected before an unsafe query.
5. Analytics or ClickHouse failures do not leak internals and map to a retryable API response.

---

### Task 1: Authenticated Analytics history query

**Files:**
- Create: `analytics/internal/domain/history.go`
- Create: `analytics/internal/usecase/telemetry/history.go`
- Create: `analytics/internal/handler/telemetry/history.go`
- Modify: `analytics/internal/application/application.go` and `routes.go`
- Modify: `analytics/internal/infrastructure/clickhouse/repository.go`
- Test: `analytics/internal/handler/telemetry/history_test.go` and `analytics/tests/integration/telemetry_history_test.go`
- Modify: `analytics/README.md`

**Interfaces:**
- `domain.HistoryCursor{MachineID string, ObservedAt time.Time, DeviceID string, EventID string}` is the validated page boundary.
- `domain.HistoryQuery{MachineID string, Limit int, Cursor *HistoryCursor}` requests one page; the cursor is optional.
- `domain.HistoryPage{Events []Event, NextCursor *string}` is the Analytics response.
- `usecase/telemetry.TelemetryHistoryRepository.List(ctx context.Context, query domain.HistoryQuery) ([]domain.Event, error)` reads ordered rows.
- `usecase/telemetry.ListHistory.Execute(ctx context.Context, query domain.HistoryQuery) (domain.HistoryPage, error)` returns items and the next cursor.
- `GET /internal/v1/machines/{machine_id}/telemetry` requires the existing internal bearer credential and supports `limit` (default 100, max 1,000) plus an optional opaque base64url JSON cursor containing machine ID, RFC3339Nano timestamp, and canonical device/event UUIDs.
- Cursor tokens are at most 512 bytes; parse and normalize timestamps to UTC microseconds, compare the ClickHouse tuple using UUID-typed values, and reject any cursor whose machine ID differs from the route.
- Analytics ClickHouse queries use a 10-second context timeout and return sanitized errors.

- [x] Add failing HTTP and ClickHouse Testcontainers tests `TestAnalyticsHistoryQueriesPages`, `TestAnalyticsHistoryRejectsInvalidCursors` and `TestAnalyticsHistoryRequiresCredential` for ordering, limit+1 lookahead, cursor continuation with same-time events from multiple devices, duplicate retry dedupe, invalid/cross-machine cursors, null optionals, auth and sanitized query failures.
- [x] Run the focused Analytics tests and confirm the query route/repository is absent.
- [x] Implement cursor validation and the history use case.
- [x] Add the ClickHouse `FINAL` query with strict descending tuple ordering, DateTime64(6) precision, UUID cursor parameters and `limit+1` lookahead for `next_cursor`.
- [x] Wire the authenticated internal route; run Analytics unit, ClickHouse integration, build and pinned lint; commit and obtain Task 1 review (`e77ac69`).

### Task 2: Owner-authorized public API proxy

**Files:**
- Create: `api/internal/domain/telemetry/history.go`
- Create: `api/internal/usecase/telemetry/history.go`
- Create: `api/internal/handler/telemetry/history.go`
- Create: `api/internal/infrastructure/telemetry/analytics/history_client.go`
- Modify: `api/internal/application/application.go` and `http.go`
- Test: `api/tests/integration/telemetry_history_test.go`
- Modify: `api/README.md` and `docs/machine-whisperer-project-design.md`

**Interfaces:**
- `domain/telemetry.HistoryItem` contains `device_id`, `event_id`, `received_at`, `schema_version`, `observed_at`, `operating_state`, `vibration_rms_g`, `dominant_frequency_hz` and `current_rms_a`; optional readings serialize as null.
- `domain/telemetry.HistoryPage{Items []HistoryItem, NextCursor *string}` is the API result DTO.
- `usecase/telemetry.HistoryClient.List(ctx context.Context, machineID string, limit int, cursor string) (domain.HistoryPage, error)` calls Analytics.
- `usecase/telemetry.ListHistory.Execute(ctx context.Context, ownerID, machineID string, limit int, cursor string) (domain.HistoryPage, error)` checks ownership before querying Analytics.
- `GET /v1/machines/{machine_id}/telemetry` requires a user JWT, defaults `limit` to 100, caps it at 1,000, returns `{items,next_cursor}`, and maps malformed input to 400, owner mismatch to 404, and Analytics transport/internal failures to 503.
- The API Analytics client has a 10-second request timeout and does not follow redirects.

- [x] Add failing API HTTP/Testcontainers tests `TestTelemetryHistoryOwnerAccess`, `TestTelemetryHistoryRejectsInvalidQuery`, `TestTelemetryHistoryAnalyticsFailureIsSanitized` and `TestTelemetryHistoryCursorForwarding` for owner access, foreign/missing machines, limit validation, cursor forwarding, sanitized Analytics failure, and stable response fields.
- [x] Run the focused integration test and confirm the public history route is absent.
- [x] Implement API query parsing, owner-scoped history use case, authenticated Analytics HTTP client and route.
- [x] Verify API does not access ClickHouse directly; run API unit, full serial Testcontainers integration, build and pinned lint; commit and obtain Task 2 review (`a5b92a7`).
- [x] Address Task 2 review finding about owner lookup failures in fix round 1 (`8b72aad`); scoped re-review passed.

## Final review

- [x] Whole-branch review found no Critical/Important issues. Final fix wave `42b7d93` added malformed cursor tests, upstream page validation and corrected the API 503 docs; scoped re-review approved.
