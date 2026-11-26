# Telemetry analytics

[![Analytics unit coverage](https://img.shields.io/endpoint?url=https%3A%2F%2Fraw.githubusercontent.com%2Fvladgrskkh%2Fmachine-whisperer%2Fcoverage-badges%2Fanalytics.json)](https://github.com/vladgrskkh/machine-whisperer/actions/workflows/ci.yml)

Analytics receives telemetry from the API outbox relay on
`POST /internal/v1/telemetry-batches`. It validates every observation, sends
one synchronous ClickHouse insert per batch, and returns HTTP 204 after
ClickHouse acknowledges it. A storage failure returns HTTP 503 so the relay
can retry.

The application protects the batch and history routes with the same internal
bearer credential and uses a shared ClickHouse repository.
The application opens the runtime ClickHouse connection at startup and closes
it at shutdown. The standalone migration command uses its own connection.

## Run the service

Run these commands from `analytics/`. Provide credentials through the
deployment environment. Create the target ClickHouse database before applying
the migration.

| Variable | Purpose |
| --- | --- |
| `CLICKHOUSE_ADDRESS` | Native TCP address, for example `clickhouse:9000` |
| `CLICKHOUSE_DATABASE` | Existing database for telemetry history |
| `CLICKHOUSE_USER` | ClickHouse account |
| `CLICKHOUSE_PASSWORD` | Password for that account |
| `ANALYTICS_INGEST_API_KEY` | Dedicated service token shared with the API |
| `ANALYTICS_ADDRESS` | HTTP listen address, defaults to `:8081` |
| `ANALYTICS_METRICS_ADDRESS` | Private Prometheus listen address, defaults to `127.0.0.1:9091`. Use a specific host and port. |

1. Run `make migrate` with a ClickHouse account that can create the table.
   The command applies the embedded, idempotent
   [table migration](migrations/001_create_telemetry_events.sql).
2. Run `make run` with a ClickHouse account that can insert into and read from
   `telemetry_events`. Startup pings the configured database.
3. Set `ANALYTICS_URL` to this service's base URL and
   `ANALYTICS_INGEST_API_KEY` to the matching token in the API environment.
   Follow the [API setup](../api/README.md) to start the API and its outbox
   worker. For cross-host deployments, terminate HTTPS on the internal
   endpoint to protect the bearer credential in transit.

The relay sends one `Authorization: Bearer <token>` header and a JSON object
containing an `events` array of 1–1,000 envelopes within a 16 MiB body.
Each envelope carries `device_id`, `event_id`, `machine_id`, `received_at`
and the original v1 observation as `payload`.

## Read telemetry history

The API reads observations through
`GET /internal/v1/machines/{machine_id}/telemetry` with the same bearer token
used for ingestion. The response contains an `items` array and a `next_cursor`
field. Each item contains the device and event IDs, the receive and observation
timestamps in UTC, and the v1 observation fields. Missing frequency or current
readings appear as JSON `null`.

The service returns the newest observations first, ordered by observation time,
device ID and event ID. The `limit` query parameter defaults to 100 and accepts
values from 1 through 1,000. Pass `next_cursor` back unchanged as the `cursor`
query parameter for the same machine. A null cursor means the end of history.
Invalid limits, machine IDs or cursors return HTTP 400.

The query uses ClickHouse `FINAL` so observations retried by the relay appear
once before background merges. ClickHouse query failures return HTTP 503
without storage details.

## Prometheus metrics

Scrape `GET /metrics` on the private listener. The endpoint reports Go and
process metrics, HTTP requests by method, route and status class, and
ClickHouse repository operation counts and durations. Authentication and body
rejections appear in the HTTP metrics. `mw_telemetry_batches_total` reports
`stored` and `storage_error` outcomes from batch ingestion.
`mw_telemetry_batch_events_stored_total` counts rows acknowledged by
ClickHouse. Successful retries add to this counter even when history returns
a single observation. ClickHouse engine metrics come from its built-in
Prometheus endpoint.

## Checks

`make build` compiles both commands. `make test` runs unit and HTTP tests.
`make test-integration` requires Docker and starts
`clickhouse/clickhouse-server:26.9.36.3`. It runs the migration command and
exercises inserts, retries, history pagination, and storage failures through
the HTTP routes.
Install the pinned linter with `make tools`, then run `make lint`.

The CI run summary lists statement coverage for the unit and integration
suites. The integration percentage covers the internal packages reached by
that suite. The unit percentage also includes the command packages. Download
the Go profiles and HTML reports from the run's Artifacts section.
