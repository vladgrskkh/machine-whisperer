# Service Application and Worker Structure Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Implement and independently review each task before the next; finish with whole-branch review. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Align Analytics application wiring with the API and run the API outbox relay as a thin in-process worker.

**Architecture:** The API application wires its export use case and starts an `internal/worker/telemetry` scheduler beside HTTP serving in the same process. Analytics gets an application composition root; its handler calls an ingest use case, which persists through a ClickHouse repository. A ClickHouse application option owns connection setup and cleanup.

**Tech Stack:** Go 1.26.5, chi, pgx, ClickHouse Go client v2.51.0, Testcontainers.

**Spec:** `docs/superpowers/specs/2026-11-23-service-application-worker-structure-design.md`

## Global Constraints

- Dependencies point inward: domain does not depend on outer layers; use cases depend on interfaces; application wiring connects concrete adapters.
- Keep the API process as the single entrypoint for HTTP and outbox relay work.
- The periodic worker only schedules calls to use cases; retry, acknowledgement and retention behavior stay in the use case.
- Analytics handlers call use cases; ClickHouse access stays behind an infrastructure repository.
- The Analytics application owns the ClickHouse connection opened through `WithClickHouse` and closes it on shutdown.
- Preserve current API/Analytics contracts, at-least-once delivery, retry semantics, ClickHouse schema and deduplication behavior.

## Review Focus

1. API cancellation and shutdown stop the worker before closing its PostgreSQL pool.
2. Worker scheduling does not duplicate or alter export/retry semantics covered by existing Testcontainers tests.
3. Analytics rejects invalid requests before calling the use case or repository.
4. ClickHouse connection open failures are sanitized and successful application startup closes the connection.
5. Analytics routes remain authenticated and respond only after ClickHouse insert succeeds.

---

### Task 1: Run the API relay as an in-process worker

**Files:**
- Create: `api/internal/worker/telemetry/worker.go`
- Modify: `api/internal/usecase/telemetry/export.go`
- Modify: `api/internal/application/application.go`
- Modify: `api/internal/application/config.go` and related config tests
- Modify: `api/cmd/api/main.go` and its tests
- Delete: `api/cmd/outbox-relay/main.go`
- Delete: `api/internal/exporter/config.go`
- Modify: `api/tests/integration/outbox_export_test.go`
- Modify: `api/README.md` and root `README.md`

**Interfaces:**
- `telemetry.Export.ProcessOnce(ctx context.Context) (int, error)` processes one batch; `PruneDelivered(ctx context.Context) (int64, error)` performs bounded delivered-row cleanup.
- `telemetryworker.Worker.Run(ctx context.Context)` periodically calls those use-case methods using configured batch, poll and retention values.
- `application.Application.RunWorkers(ctx context.Context)` runs the configured worker; `cmd/api` starts it under the same signal context as the HTTP server and waits for shutdown.

- [x] Add a worker test proving it invokes the use case on startup and exits on cancellation.
- [x] Run the new worker test and confirm the expected failure before implementation.
- [x] Move periodic scheduling and retention cadence out of the use case into `internal/worker/telemetry` while keeping delivery and retry logic in the use case.
- [x] Load relay settings through the API application's config; wire PostgreSQL outbox repository, Analytics HTTP sender, export use case and worker from `application.New`.
- [x] Start and join the worker in the existing `cmd/api` lifecycle; remove the standalone relay entrypoint and update setup documentation.
- [x] Run API unit, integration and pinned lint checks; review and commit this task (`ea5fe4e`).

### Task 2: Give Analytics the application/usecase/repository structure

**Files:**
- Create: `analytics/internal/application/application.go`, `routes.go`, `clickhouse.go`, and application tests
- Create: `analytics/internal/usecase/telemetry/ingest.go`
- Create: `analytics/internal/handler/telemetry/batch.go`
- Create: `analytics/internal/handler/middleware/bearer.go`
- Create: `analytics/internal/infrastructure/clickhouse/repository.go`
- Modify: `analytics/internal/infrastructure/clickhouse/client.go`
- Modify: `analytics/cmd/analytics/main.go`
- Modify: Analytics handler tests and `analytics/README.md`

**Interfaces:**
- `usecase.TelemetryRepository.StoreBatch(ctx context.Context, events []domain.Event) error` persists a validated batch.
- `usecase.IngestBatch.Execute(ctx context.Context, events []domain.Event) error` delegates to the repository.
- `clickhouse.Repository` implements `TelemetryRepository`.
- `application.WithClickHouse(config.ClickHouse) application.Option` opens the connection, wires the repository and registers cleanup; `application.WithTelemetryRepository` supports tests.
- `application.Application.Routes() http.Handler` registers the authenticated batch route and injects its handler/use case.

- [x] Add Analytics application/handler tests showing valid requests call the use case and invalid/auth-failing requests do not reach persistence.
- [x] Run the new tests and confirm the expected failure before implementation.
- [x] Move batch HTTP behavior into a domain handler that depends on the ingest use case, not a ClickHouse writer interface.
- [x] Add the repository and application composition/options; remove direct ClickHouse open and handler wiring from `cmd/analytics`.
- [x] Keep migration CLI behavior intact; update service docs to describe the application-based setup.
- [x] Run Analytics unit, ClickHouse Testcontainers integration and pinned lint checks; review and commit this task (`7df4e2c`).
