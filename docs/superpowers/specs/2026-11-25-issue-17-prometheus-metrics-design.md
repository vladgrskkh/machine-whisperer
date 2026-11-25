# Prometheus metrics for the application and infrastructure

Approved, 2026-11-26. [Issue #17](https://github.com/vladgrskkh/machine-whisperer/issues/17)

## Decision

Give each Go process a private `GET /metrics` listener. Have Prometheus directly scrape both processes, Nginx, PostgreSQL, ClickHouse and itself. An operator should be able to follow an observation from API acceptance through outbox delivery to ClickHouse, see when a detector opens or recovers an alert, and distinguish a silent service from an empty queue.

Both services use Chi and expose `/healthz`. The API runs its outbox worker in the API process, logs HTTP requests with `httplog`, and logs relay outcomes with `slog`. Analytics uses a native ClickHouse connection for batch writes and history reads. Prometheus currently scrapes only itself. The commented application jobs would require extra network access and trust for the API's TLS listener. [API wiring](../../../api/internal/application/application.go) · [Analytics wiring](../../../analytics/internal/application/application.go) · [Current scrape config](../../../deploy/prometheus/prometheus.yml)

The project design shows a Collector exporting service metrics to Prometheus. Direct pull is a proposed amendment for #17. It gives an independent scrape-health series for each process and can ship before tracing. An alternative is for an OTel Collector to scrape these endpoints and forward their metrics, which adds a deployment dependency and requires preserving per-target identity through export. Scraping the existing business listeners would save two listeners but still require a network change and would tie API metrics to its certificate configuration. Approval of direct pull should include updating the architecture diagram during implementation and enabling exactly one ingestion path per series. [Project observability design](../../machine-whisperer-project-design.md#service-observability)

## Connectivity and access

| Component | Network membership after #17 | Metrics access |
| --- | --- | --- |
| API | `edge`, `data`, `scrape` | Bind the metrics listener to `api-metrics:9091`, using a DNS alias defined only on `scrape`. |
| Analytics | `data`, `scrape` | Bind to `analytics-metrics:9091` under the same rule. |
| Prometheus | `monitoring`, `scrape`, `infra-scrape` | Reach the two Go listeners and three infrastructure targets. Retain its self-scrape and host UI binding on `127.0.0.1:9090`. |
| Nginx | `edge` and an exporter-only status network | Serve `stub_status` on a separate listener restricted to the exporter. Its existing HTTPS fallback returns 404 for `/metrics`. |
| PostgreSQL, ClickHouse and migrations | `data` | An exporter reads PostgreSQL statistics, and a fixed-path proxy forwards ClickHouse metrics. Prometheus reaches their metrics listeners on `infra-scrape`. |

Create `scrape` as an internal Docker bridge. Bind to the resolved scrape-interface IP, fail startup if it cannot bind, and leave the metrics port unpublished. A wildcard bind on a multi-homed service would also listen on `edge` or `data`. Use explicit per-service metrics-listen configuration, defaulting to loopback for local runs. The scrape listener serves metrics only, with bounded HTTP timeouts, and participates in the same startup error handling and shutdown deadline as the business server. Existing wildcard business listeners are reachable by the trusted scraper via this bridge and retain their current authentication. Restrict bridge membership to these three containers. Keep Nginx’s public allowlist and deployment firewall ingress limited to existing public ports. [Compose](../../../deploy/compose.yaml) · [Nginx HTTPS routing](../../../deploy/nginx/default.conf.template)

Use job names `api` and `analytics`, HTTP path `/metrics`, a 15-second interval and a 5-second scrape timeout. The proposed trust boundary for unauthenticated HTTP is one operator-controlled Docker host and the dedicated bridge. For cross-host or shared-tenant scraping, require verified TLS and a dedicated scrape credential or mTLS with restricted ingress before enabling that topology. Application bearer tokens, device credentials and database passwords stay with their existing clients.

## Infrastructure scrape jobs

| Job | Source | First metrics to check |
| --- | --- | --- |
| `nginx` | Nginx OSS Prometheus exporter backed by a private `stub_status` listener | `nginx_up`, total requests, active connections and reading, writing and waiting connections |
| `postgres` | `postgres_exporter` using a dedicated PostgreSQL login with statistics access | `pg_up`, active connections, transaction commits and rollbacks, cache hits and reads, deadlocks and database size |
| `clickhouse` | ClickHouse's native Prometheus endpoint, via the fixed-path private proxy | `ClickHouseMetrics_*`, `ClickHouseProfileEvents_*`, and selected `ClickHouseAsyncMetrics_*` for merges, query volume, memory and disk |

Keep exporter metrics ports unpublished. Prometheus reaches the exporters and
metrics proxy through an internal `infra-scrape` bridge. The PostgreSQL exporter
connects on `data` using its own file-backed secret and statistics privileges
such as `pg_monitor`. Enable the role on fresh and existing databases. Avoid
query-text collectors and verify the selected collectors on PostgreSQL 18.
[PostgreSQL exporter](https://github.com/prometheus-community/postgres_exporter)
· [PostgreSQL predefined roles](https://www.postgresql.org/docs/18/predefined-roles.html)

The ClickHouse proxy is the only bridge between its native metrics listener
and the scraper. Forward only `GET /metrics`, propagate upstream failures,
and keep the ClickHouse HTTP query and native protocol ports on `data`.
Start with metrics, events and asynchronous metrics. Enable additional
families after checking cardinality and scrape cost on the pinned version.
Use the same 15-second interval and 5-second timeout for infrastructure jobs
as the Go jobs, and record scrape cost before tuning.
[ClickHouse server configuration](https://github.com/ClickHouse/ClickHouse/blob/master/programs/server/config.xml) · [ClickHouse metric families](https://github.com/ClickHouse/ClickHouse/blob/master/src/Server/PrometheusMetricsWriter.cpp)
· [Nginx exporter](https://github.com/nginx/nginx-prometheus-exporter)

## Metric contract

Use one explicit Prometheus registry per application instance. Register the Go and process collectors and expose that registry with `promhttp.HandlerFor`. Pass small observation interfaces or typed results through the existing application wiring so domain objects stay independent of the metrics library. Use Prometheus-generated `job` and `instance` labels. Both modules publish the same names for common HTTP metrics. [Go handler](https://pkg.go.dev/github.com/prometheus/client_golang/prometheus/promhttp) · [Target labels and scrape metrics](https://prometheus.io/docs/concepts/jobs_instances/)

| Scope | Series | Type and labels | Count or measure |
| --- | --- | --- | --- |
| Both | `go_*`, `process_*` | Standard collector families | Goroutines, GC, heap, resident memory, CPU time and process start time on Linux. |
| Both | `mw_http_requests_total` | Counter, `route,method,status_class` | Completed business-listener requests, including auth failures and unmatched paths. |
| Both | `mw_http_request_duration_seconds` | Histogram, `route,method` | Wall time through HTTP middleware and handler. |
| Both | `mw_http_requests_in_flight` | Gauge | Current business-listener requests. Track health checks under their route and keep periodic scrapes outside these three families. |
| API | `mw_telemetry_ingest_requests_total` | Counter, `outcome` | One POST to the telemetry route, including early rejection. Values: `accepted_new`, `accepted_replay`, `invalid`, `unauthorized`, `forbidden`, `machine_not_found`, `conflict`, `outside_window`, `out_of_order`, `internal_error`. |
| API | `mw_outbox_pending_events`, `mw_outbox_oldest_pending_age_seconds` | Gauges | Count of all undelivered rows and elapsed time since the oldest was enqueued, including leased and delayed rows. Both zero for a successful empty snapshot. |
| API | `mw_outbox_retry_events`, `mw_outbox_oldest_retry_age_seconds` | Gauges | Undelivered rows with a recorded prior send failure (`last_error_category` set) and age since the oldest of these rows was enqueued. Zero on a successful empty set. |
| API | `mw_outbox_snapshot_timestamp_seconds`, `mw_outbox_snapshot_errors_total` | Gauge and counter | Unix time of last successful queue snapshot and count of failed refreshes. |
| API | `mw_outbox_claimed_events_total`, `mw_outbox_delivered_events_total` | Counters | Count rows actually claimed (including reclaims), and rows successfully marked delivered under the current lease. Use a rate of the latter for export throughput. |
| API | `mw_outbox_retried_events_total` | Counter, `reason` | Rows actually rescheduled. Reasons: `analytics_http`, `analytics_timeout`, `analytics_transport`, `analytics_request`. |
| API | `mw_outbox_send_attempts_total`, `mw_outbox_send_duration_seconds` | Counter and histogram, `outcome` | One observation per nonempty HTTP batch send. Values: `success`, one of the four send-failure reasons, or `canceled`. |
| API | `mw_outbox_operation_errors_total` | Counter, `operation` | Failed database operations: `claim`, `acknowledge`, `reschedule`, `prune`. |
| API | `mw_detector_observations_total` | Counter, `outcome` | One committed new observation: `evaluated`, `no_baseline`, or `unknown_state`. Check for a baseline first, matching the detector. |
| API | `mw_alert_transitions_total` | Counter, `transition` | `opened`, `recovered`, `acknowledged`. Count the committed transition once, including first acknowledgement only. |
| API | `mw_postgres_pool_connections` | Gauge, `state` | Local pgx pool statistics for `acquired`, `idle` and `constructing`. |
| Analytics | `mw_telemetry_batches_total` | Counter, `outcome` | One request on the batch endpoint. Values: `stored`, `invalid`, `too_large`, `unauthorized`, `storage_error`. |
| Analytics | `mw_telemetry_batch_events_stored_total` | Counter | Number of event rows in batches acknowledged by ClickHouse. A successful replay can add rows again. |
| Analytics | `mw_clickhouse_operations_total` | Counter, `operation,outcome` | One repository call for `insert_batch` or `list_history`. Outcomes: `success`, `timeout`, `canceled`, `error`. |
| Analytics | `mw_clickhouse_operation_duration_seconds` | Histogram, `operation` | Include prepare, append and send for a batch and query, scan and iteration completion for history. |
| Analytics | `mw_clickhouse_pool_connections` | Gauge, `state` | Expose the pinned native driver's `Stats().Open` and `Stats().Idle` using the corresponding `open` and `idle` labels as reported by that driver. |
| Prometheus | `up`, `scrape_duration_seconds`, `scrape_samples_scraped` | Target-generated gauges | Scrape success, duration and sample volume for each job and instance. |

For HTTP, resolve the registered Chi route template even for auth denials. Normalize unmatched routes to `unmatched`, methods to the registered method set plus `OTHER`, and statuses to their HTTP class. Exclude raw paths, query strings, user/device/machine/event/alert IDs, trace IDs, tokens, SQL and raw errors from labels. Use common classic latency buckets in seconds: 0.005, 0.025, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 25, 30, 60 and the implicit +Inf. The upper buckets cover existing ClickHouse insert and relay HTTP deadlines. Initialize fixed business-counter outcomes to zero so idle workloads remain queryable.

## Counting and failure semantics

- **Telemetry and alerts.** Both a new event and a same-payload replay currently return HTTP 202. Carry the internal disposition and detector outcome out of the transaction, then update counters after successful commit. Rollback produces one request failure and zero new events or alert transitions. At the authenticated route boundary, classify decode/auth/use-case failures once. Alert acknowledgement currently uses `COALESCE`, so a repeat can affect one SQL row. Have persistence report the first state change atomically while retaining successful idempotent retries and the existing missing/ownership response. [Ingest](../../../api/internal/usecase/telemetry/ingest.go) · [Detector](../../../api/internal/usecase/telemetry/detector.go) · [Acknowledgement](../../../api/internal/infrastructure/alert/postgres/repository.go)
- **Relay.** A send success can precede a lost lease or failed database acknowledgement. Count `MarkDelivered`'s affected rows and extend `Reschedule` to report affected rows for each retry-delay group. A canceled send left for lease expiry contributes zero reschedules. These are process counters and can lose the last increment across a crash. The queue snapshot is the database-backed signal for outstanding work. [Relay use case](../../../api/internal/usecase/telemetry/export.go) · [Lease and retry SQL](../../../api/internal/infrastructure/telemetry/postgres/outbox.go)
- **Queue freshness.** Run a read-only aggregate at startup and every 30 seconds with a 2-second timeout, independent of the relay poll loop. Use one database timestamp per snapshot. Publish all gauges together. On failure retain the last successful values and timestamp, increment the error counter and continue serving scrapes. Before first success expose timestamp zero and omit queue gauges. Limit the aggregate to undelivered rows and assess its plan on a representative backlog. A database outage after startup should leave runtime and HTTP metrics scrapeable.
- **ClickHouse.** Classify repository outcomes at return, including row iteration errors and deadline expiry. The Analytics batch response counts requests, while the stored-event counter counts acknowledged physical writes across attempts. These units let operators diagnose repeat delivery without treating a write attempt as a unique observation. [Repository](../../../analytics/internal/infrastructure/clickhouse/repository.go) · [Batch handler](../../../analytics/internal/handler/telemetry/batch.go)

## Consumers and issue boundaries

The operator can compare new device observations and acknowledged export rates with `sum(rate(mw_telemetry_ingest_requests_total{outcome="accepted_new"}[5m]))` and `sum(rate(mw_outbox_delivered_events_total[5m]))`. Read these alongside queue age and `time() - mw_outbox_snapshot_timestamp_seconds`. On multiple API replicas, queue snapshots observe the same Postgres rows, so use a fresh instance or the maximum of fresh snapshots rather than summing gauges. Counters remain per-process.

Issue #10 owns distributed tracing, propagation and the trace export path. Coordinate route templates and service names (`api`, `analytics`) so operators can move between signals. Issue #17 owns the Go metric contract and all scrape jobs described above. Issue #18 consumes these series for dashboards. Database metrics describe the engines, while the Go pool and query metrics describe what the services observe. Alert thresholds and paging can follow separately. [Tracing #10](https://github.com/vladgrskkh/machine-whisperer/issues/10) · [Dashboards #18](https://github.com/vladgrskkh/machine-whisperer/issues/18)

## Acceptance checks for implementation

1. In both modules, create two app instances with independent registries. Exercise the normal routers and metrics handlers over HTTP, parse the exposition, check family names/types/units and exact counter deltas, and vary random IDs, paths and methods to verify label bounds. Check early auth rejection, 404/405, health traffic and concurrent requests.
2. Extend the API PostgreSQL Testcontainers cases for new/replay/conflict/out-of-order input, rollback, alert open/recovery/idempotent acknowledgement, empty vs failed snapshots, failed send, reclaimed lease and final acknowledged row count. Verify stored outcomes and metric deltas agree.
3. Extend Analytics ClickHouse Testcontainers cases with an actual batch write, history read, invalid input, repeated write and a repository failure. Verify operations, rows and outcomes on the service endpoint. Confirm previous request contracts and data remain valid.
4. Run existing Go race/unit, integration and build CI jobs. In deploy CI, validate effective config with the pinned Prometheus image's `promtool check config`. Update Compose network assertions and keep the published-port allowlist. Extend smoke CI to wait for exactly one successful target per configured job, and query service metrics through Prometheus. Stop one target, observe its `up` reach zero, restore it and require recovery within three scrapes. Verify public HTTPS `/metrics` still returns 404, metrics cannot be reached at service IPs on `edge` or `data`, and a probe solely on `scrape` cannot reach either database. [CI workflow](../../../.github/workflows/ci.yml) · [Compose assertions](../../../deploy/scripts/test-compose-config.sh) · [Smoke test](../../../deploy/scripts/smoke-test.sh)

5. In the disposable stack, query `nginx_up`, `pg_up` and representative ClickHouse native families through Prometheus. Verify a database outage is visible in the matching exporter or proxy and that Prometheus and Grafana cannot reach the database query ports.

## Decisions for review

1. Approve direct Prometheus pull as the metrics path alongside Collector-based traces in #10?
2. Is the single-host, trusted-bridge assumption sufficient for the first deployment, or do operators require authenticated encrypted scrapes at launch?
3. Does 30-second queue freshness meet operational needs? A shorter interval increases the cost of exact backlog aggregation during an outage.
