# Service Application and Worker Structure

**Status:** Approved by the user for implementation in PR #8

## Intent

Keep the new Analytics service organized like the API service and run the API
outbox relay within the API application's lifecycle. This avoids a second API
executable and keeps periodic scheduling separate from export business logic.

## Goals

- Give Analytics an `internal/application` composition root with explicit
  routes, handlers, use cases and infrastructure.
- Make ClickHouse persistence a repository used by a batch-ingest use case.
- Open ClickHouse through an application option and close it with the app.
- Move API periodic polling to `internal/worker/telemetry/worker.go`; the
  worker calls the export use case and contains scheduling only.
- Start and stop the worker with the API process; keep `cmd/api` as the only
  API executable.

## Non-goals

- Changing telemetry batch format, validation, delivery, retry, schema or
  ClickHouse behavior.
- Adding baseline analysis or alert detection.
- Changing the independent Analytics migration command.

## Approaches

1. Keep a separate `cmd/outbox-relay` executable — rejected because it creates
   a second API deployment entrypoint for background work.
2. Keep scheduling inside the export use case — rejected because it mixes
   periodic process lifecycle with export business behavior.
3. Use one API process and a thin `internal/worker/telemetry` scheduler that
   periodically calls the export use case; compose Analytics routes and
   dependencies in `internal/application`, injecting a ClickHouse repository
   opened by an application option — selected by the user to match the API's
   structure.

## Design

The API application owns one outbox export use case, one telemetry worker and
the HTTP server lifecycle. `cmd/api` starts the worker alongside the server
under the same signal context and waits for it to stop before closing shared
resources. The worker owns tick and retention-prune timing only; export,
acknowledgement and retry logic remain in the use case.

Analytics `internal/application` wires its chi routes, HTTP handler,
batch-ingest use case and ClickHouse repository. The handler validates and
decodes input, then calls the use case. The use case depends on a small
repository interface. An `application.WithClickHouse` option opens ClickHouse,
creates the repository and registers connection cleanup. `cmd/analytics` loads
config and runs the application. The migration CLI remains a separate command.

## Open choices settled by implementation

- Configuration and startup validation follow the existing API application
  pattern.
- The worker runs on the API process's cancellation context and logs through
  the injected application logger.
- Tests inject a repository or use the existing PostgreSQL/ClickHouse
  Testcontainers integration suites.
