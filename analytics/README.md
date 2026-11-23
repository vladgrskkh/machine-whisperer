# Telemetry analytics

Analytics receives telemetry from the API outbox relay on
`POST /internal/v1/telemetry-batches`. It validates every observation, sends
one synchronous ClickHouse insert per batch, and returns HTTP 204 after
ClickHouse acknowledges it. A storage failure returns HTTP 503 so the relay
can retry.

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
| `ANALYTICS_INGEST_API_KEY` | Dedicated service token shared with the API relay |
| `ANALYTICS_ADDRESS` | HTTP listen address, defaults to `:8081` |

1. Run `make migrate` with a ClickHouse account that can create the table.
   The command applies the embedded, idempotent
   [table migration](migrations/001_create_telemetry_events.sql).
2. Run `make run` with a ClickHouse account that can insert into
   `telemetry_events`. Startup pings the configured database.
3. In the API module, set `DATABASE_URL` for the outbox,
   `ANALYTICS_URL` to this service's base URL, and
   `ANALYTICS_INGEST_API_KEY` to the matching token. Start the relay with
   `go run ./cmd/outbox-relay`. For cross-host deployments, terminate HTTPS
   on the internal endpoint to protect the bearer credential in transit.

The relay sends one `Authorization: Bearer <token>` header and a JSON object
containing an `events` array of 1–1,000 envelopes within a 16 MiB body.
Each envelope carries `device_id`, `event_id`, `machine_id`, `received_at`
and the original v1 observation as `payload`.

## Read telemetry history

A relay that loses the acknowledgement after an insert can send the same events
again. The table uses `ReplacingMergeTree(received_at)`, partitions by month of
observation, and replaces rows with the same
`(machine_id, observed_at, device_id, event_id)` sorting key during background
merges. Use `FINAL` for a deduplicated view immediately after retries.

```sql
SELECT observed_at, vibration_rms_g, dominant_frequency_hz, current_rms_a
FROM telemetry_events FINAL
WHERE machine_id = {machine_id:UUID}
  AND observed_at >= {since:DateTime64(6, 'UTC')}
ORDER BY observed_at;
```

## Checks

`make build` compiles both commands. `make test` runs unit and HTTP tests.
`make test-integration` requires Docker and starts
`clickhouse/clickhouse-server:26.9.36.3`. It runs the migration command and
exercises inserts, retries and storage failures through the HTTP endpoint.
Install the pinned linter with `make tools`, then run `make lint`.
