# Grafana dashboards for the Compose deployment

Approved, updated 2026-11-26. [Issue #18](https://github.com/vladgrskkh/machine-whisperer/issues/18)

An operator investigating delayed readings should be able to tell whether requests reached Nginx, whether the API accepted them, whether the relay is falling behind, and whether Analytics can write to ClickHouse. Provision five dashboards from Git for a fresh installation and for upgrades. One Go dashboard serves both API and Analytics through distinct, bookmarkable service selections.

## Starting point and dependencies

Prometheus scrapes itself every 15 seconds and stores samples in `prometheus-data`. Grafana is absent. The API and Analytics serve health endpoints, and neither exposes Prometheus metrics. Only Nginx publishes public ports. Its HTTPS template proxies the API and manufacturing endpoints, disables access logs and has no status listener. PostgreSQL 18 and ClickHouse 26 run on the internal `data` network. [Compose](../../../deploy/compose.yaml) · [Scrape configuration](../../../deploy/prometheus/prometheus.yml) · [Nginx](../../../deploy/nginx/default.conf.template) · [API routes](../../../api/internal/application/http.go) · [Analytics routes](../../../analytics/internal/application/routes.go)

Issue #17 owns Go instrumentation, metric names, labels and the application and infrastructure scrape jobs. Issue #18 consumes that contract in provisioned dashboards. Review those interfaces together before implementing panel queries. Issue #10 owns tracing from Nginx and both services through the Collector to Jaeger. After #10 lands, optionally provision the built-in Jaeger data source at `http://jaeger:16686` for Explore, searching by service and time range. Metrics dashboards work independently. An exact trace link would require a separate exemplar contract. Alert rules, paging and changes to product alerts are separate work. [Metrics design](2026-11-25-issue-17-prometheus-metrics-design.md) · [Tracing design](2026-11-25-issue-10-otel-tracing-design.md) · [Project observability design](../../machine-whisperer-project-design.md#service-observability)

## Dashboard inventory

| Dashboard / stable UID | Operator view and proposed source |
| --- | --- |
| Nginx / `mw-nginx` | Request rate and active, reading, writing and waiting connections. Show Prometheus scrape success and the exporter's `nginx_up` separately so a failed status fetch is visible. Adapt the [NGINX OSS exporter dashboard, v1.5.3](https://github.com/nginx/nginx-prometheus-exporter/blob/v1.5.3/grafana/dashboard.json). OSS stub status supplies aggregate traffic. Route latency and status classes belong on the Go view at the application boundary. |
| Go services / `mw-go-services` | Reuse compatible panels from [Go Processes, ID 6671](https://grafana.com/grafana/dashboards/6671-go-processes/). Select `job=api` or `analytics`, then instance. Show scrape health, goroutines, heap, process CPU and RSS, HTTP requests by status class, in-flight requests and p95 by route. Provide explicit API and Analytics links using `var-job`, carrying the selected time range. Exclude health traffic from the default HTTP workload view. |
| PostgreSQL / `mw-postgres` | Database availability, connections, transactions, rollbacks, deadlocks and database size for `machine_whisperer`. Adapt the [postgres_exporter mixin overview, v0.21.1](https://github.com/prometheus-community/postgres_exporter/blob/v0.21.1/postgres_mixin/dashboards/postgres-overview.json). Check every collector and label against PostgreSQL 18 and the pinned exporter. Display exporter scrape health separately from `pg_up`. |
| ClickHouse / `mw-clickhouse` | Native engine queries and failures, inserted rows, current memory use and merges where the pinned server exposes them. Start with ClickHouse's [Prometheus instance dashboard, ID 23415 revision 3](https://grafana.com/grafana/dashboards/23415-prom-exporter-instance-dashboard-v2/). Keep panels backed by the single-node native endpoint. Drop cloud-only variables, replica and Keeper panels. |
| Telemetry pipeline / `mw-telemetry` | Project-owned JSON. Follow new device observations, accepted replays, ingest use-case rejections and errors, pending and retry depth/age, successful relay delivery, batch storage outcomes, detector outcomes and alert transitions. Link to API, Analytics and both databases. Distinguish requests, outbox rows marked delivered and rows acknowledged by ClickHouse in the panel units. Batches can be replayed, so stored rows can exceed unique observations. |

## Application metric inputs from #17

Bind application queries to their scrape job. The PostgreSQL and ClickHouse dashboards provide engine diagnostics from the infrastructure scrape jobs.

| View | Required series and dimensions |
| --- | --- |
| Both Go services | Prometheus `up{job,instance}`, Go/process collectors including `go_goroutines`, `go_memstats_heap_alloc_bytes`, `process_cpu_seconds_total` and `process_resident_memory_bytes`. HTTP uses `mw_http_requests_total{route,method,status_class}`, `mw_http_request_duration_seconds_bucket{route,method,le}` and `mw_http_requests_in_flight`. The general HTTP request counter includes requests rejected during authentication, authorization or decoding, and oversized Analytics batches rejected before storage. |
| API ingest and detector | `mw_telemetry_ingest_requests_total{outcome}` distinguishes `accepted_new`, `accepted_replay` and ingest use-case rejection and error outcomes. `mw_detector_observations_total{outcome}` counts committed observations. `mw_alert_transitions_total{transition}` counts opens, recoveries and acknowledgements. |
| API relay | `mw_outbox_pending_events`, `mw_outbox_oldest_pending_age_seconds`, `mw_outbox_retry_events`, `mw_outbox_oldest_retry_age_seconds`, `mw_outbox_snapshot_timestamp_seconds`, `mw_outbox_snapshot_errors_total`, `mw_outbox_delivered_events_total` and `mw_outbox_retried_events_total{reason}`. |
| Analytics storage and ClickHouse client operations | `mw_telemetry_batches_total{outcome}` counts `stored` and `storage_error` outcomes. Use `mw_telemetry_batch_events_stored_total` for acknowledged rows and `mw_clickhouse_operations_total{operation,outcome}` and `mw_clickhouse_operation_duration_seconds_bucket{operation,le}` for client-observed outcomes and timings. |

For HTTP p95, apply `rate` over `$__rate_interval`, sum bucket rates by `le,route` for the selected service/instance, then apply `histogram_quantile(0.95, …)`. A five-minute new-observation rate is `sum(rate(mw_telemetry_ingest_requests_total{job="api",outcome="accepted_new"}[5m]))`. Idle latency windows display “No observations”.

The API reads the queue once per scrape. A failed read emits a zero timestamp and omits the queue gauges. Select snapshots from live API instances with a positive timestamp less than 90 seconds old, and use the maximum across fresh instances observing the same database. Show snapshot age and read failures alongside queue depth. A fresh, empty queue displays zero. Missing or stale snapshots display “Unknown” in current queue stats and gaps in time series.

## Infrastructure inputs

Use the #17 targets `nginx`, `postgres` and `clickhouse`. Grafana connects to Prometheus on `monitoring` for the infrastructure views. It receives public proxy traffic through a separate bridge shared only by Grafana and Nginx. Dashboard panels query the native or exporter series described in the [Prometheus infrastructure scrape design](2026-11-25-issue-17-prometheus-metrics-design.md#infrastructure-scrape-jobs).

Review the exposed series on the pinned images before importing templates. Remove panels that require host agents, extra database privileges, unbounded query text or unsupported recording rules. If an essential signal is missing, agree on it with the producer before shipping that panel.

## Provisioning and operation

Use classic file provisioning with one read-only, default Prometheus data source (UID `mw-prometheus`, server URL `http://prometheus:9090`, scrape interval 15s) and a file provider for the “Machine Whisperer” folder. Keep data source/provider YAML under `deploy/grafana/provisioning/`, and ready-to-load JSON under `deploy/grafana/dashboards/`. Mount these paths read-only. Set `allowUiUpdates: false`. Poll files every 30 seconds and reconcile dashboard removals through Git. Persist accounts and preferences in a `grafana-data` volume. Include it in the existing backup/restore runbook. [Grafana provisioning](https://grafana.com/docs/grafana/latest/administration/provisioning/)

Check in each adapted template at a specific upstream commit or catalog revision. Record its source, revision, original checksum, license/attribution and local edits beside the dashboards. Resolve import-time datasource placeholders to `mw-prometheus` in panel targets, variables and annotations. Assign stable UIDs, remove numeric database IDs, migrate legacy panel types and substitute supported raw queries for missing recording rules. Vendor compatible reusable panels only where redistribution is permitted. Pin the Grafana image and test that version with the chosen JSON. A manual catalog import per installation saves packaging effort but makes restores dependent on external services and leaves local edits unreviewed.

Default to a six-hour window and a 30-second panel refresh. Prometheus owns metric history. Propose explicitly retaining 15 days in its existing volume, with a size cap sized to the host after measuring sample growth and reserving headroom for WAL and compaction. The earlier time or size limit determines available history. Trace retention belongs to #10. [Prometheus storage](https://prometheus.io/docs/prometheus/latest/storage/)

Serve Grafana on the deployment's public HTTPS origin under `/grafana/`.
Nginx reuses its existing certificate and redirects `/grafana` to `/grafana/`.
Proxy the Grafana prefix and its Live WebSocket traffic over the dedicated
bridge. Configure Grafana's root URL for the external HTTPS origin and
subpath, enable secure session cookies, and disable anonymous access and user
signup. The Grafana container has an internal listener. [Grafana reverse proxy setup](https://grafana.com/tutorials/run-grafana-behind-a-proxy/)

Bootstrap the Grafana admin password through a dedicated generated Compose
secret using `GF_SECURITY_ADMIN_PASSWORD__FILE`. Support additive secret
creation for existing deployments. On first deployment, create a named
Grafana user and assign the Viewer role for dashboard access. Keep the admin
account for provisioning and account management. Use Grafana's user management
flow to set or rotate passwords and keep the initial secret in the deployment's
secret directory. Grafana reads metrics through the provisioned Prometheus
data source. [Grafana user roles](https://grafana.com/docs/grafana/latest/administration/user-management/user-roles/)

## Implementation acceptance

1. Start the stack with pinned images, generated secrets and an empty Grafana volume. Check data source health and fetch all five dashboards by UID as an authenticated operator without a catalog download or manual import. Recreate Grafana against the existing volume, confirm each UID appears once and verify both Go links select the intended job.
2. Validate Compose network, published-port and secret assertions, Nginx config in both modes, and Prometheus config. Check HTTPS subpath routing, secure cookies, rejection of unauthenticated dashboard and datasource API requests, and Viewer access without dashboard or user administration. Verify public status paths and database port isolation from Grafana and Prometheus. Exercise role/secret setup on a populated deployment as well as a fresh one.
3. With the #17 contract present, generate seeded ingest, replay, export and history traffic. Evaluate the actual provisioned panel expressions and variables through Grafana's data source against the live pinned producers. Check finite values and correct units for each dashboard and each Go service, with at least two scrapes for rates. Observe a new acceptance and a replay, a delivered row, a batch write, and a history read. Verify all operational panels resolve without missing dependencies.
4. In a disposable stack, stop an exporter, interrupt one upstream and pause one Go service in turn. Confirm loss of scrape health, upstream health or snapshot freshness is visible in its corresponding panel. Restore the targets and check recovery. Bring Grafana down independently and confirm ingestion/export continue.

## Implementation defaults

Use one shared Go dashboard with separate API and Analytics links. Start with a 15-day metric history and choose a size cap using the observed storage growth and available disk budget.
