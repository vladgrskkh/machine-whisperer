# Distributed tracing for Machine Whisperer

Approved · 2026-11-26 · [Issue #10](https://github.com/vladgrskkh/machine-whisperer/issues/10)

An operator should be able to open a slow history request in Jaeger and see how much time Nginx, the API, PostgreSQL calls, Analytics and ClickHouse calls consumed. For device ingestion, two linked traces show when the API accepted a reading and what happened on the later attempt to deliver its batch.

## Recommended deployment

Use the native Nginx OpenTelemetry module and the Go OpenTelemetry SDKs. All three processes push OTLP/gRPC to a single OpenTelemetry Collector, which filters and batches spans and exports OTLP/gRPC to a single Jaeger v2 instance. This follows the [project design](../../machine-whisperer-project-design.md).

| Approach | Tradeoff |
| --- | --- |
| **Collector and Jaeger** | One extra process provides a common point to remove Nginx default attributes, bound retries and change the backend independently of applications. |
| Direct OTLP to Jaeger | Jaeger v2 can receive OTLP. This saves a container but puts shared filtering and ingestion policy inside the storage/query deployment. |
| Native server telemetry | PostgreSQL statistics and ClickHouse query diagnostics help investigate database internals. Neither reconstructs the API call, HTTP parentage or outbox delivery. Native Nginx access logs are currently disabled. |

## Request paths and span ownership

| Path | Expected trace |
| --- | --- |
| History | Nginx edge span → API `GET /v1/machines/{machine_id}/telemetry`. The API checks ownership in PostgreSQL, then calls Analytics. Its outbound HTTP span parents the Analytics server span on `GET /internal/v1/machines/{machine_id}/telemetry`, which owns the ClickHouse history-read span. A failed ownership check ends at the API. |
| Acceptance | Nginx → API `POST /v1/devices/me/telemetry` → `telemetry.ingest` → PostgreSQL operations for deduplication, detector updates and enqueue. Device verification happens under the HTTP span. The compatibility POST under `/v1/machines/{machine_id}/telemetry` uses the same use case span. Acceptance ends after commit. |
| Delivery | API worker attempt → outbound HTTP → Analytics `POST /internal/v1/telemetry-batches` → ClickHouse batch insert. After a successful response the worker marks rows delivered in PostgreSQL. A failed send records the bounded failure category and the reschedule operation when it occurs. |

**Nginx.** Select a pinned image containing an ABI-compatible `ngx_otel_module` and load it at main scope. Verify the supported image/module combination before changing the current slim image. On the two proxied TLS locations, create a local root and use `otel_trace_context inject`. Replace caller-supplied trace headers and strip baggage, including on unsampled requests. Keep names fixed by location. Preserve upstream certificate verification, manufacturing CIDR checks, redirects and the certificate bootstrap template. ACME traffic, rejected fallback paths and health probes need no spans. The module emits OTLP/gRPC and includes sensitive attributes such as `http.target` by default. [Nginx module reference](https://nginx.org/en/docs/ngx_otel_module.html)

**Go HTTP.** Use `otelchi` middleware on both Chi routers and `otelhttp.NewTransport` on both API HTTP clients that call Analytics. Resolve server span names after Chi routes the request so escaped parameters use the matched template. Normalize legacy `otelchi` attributes before they reach the SDK. The API accepts a remote parent only from its configured proxy peer, determined from the socket address. Direct callers get a new root. Analytics accepts valid trace context on its private API connection and continues to require its service bearer token. Invalid or missing context starts a local root. Propagate cancellation and keep the clients' timeouts and redirect policy. Skip health probes and future metrics scrapes. Resource names are `nginx`, `api` and `analytics`, with `service.namespace=machine-whisperer`. [otelchi](https://github.com/riandyrn/otelchi) · [OpenTelemetry HTTP transport](https://pkg.go.dev/go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp)

**Database boundaries.** Attach a pgx tracer to the API pool for active request or worker spans. Emit client spans with a small allowlist of operation names and PostgreSQL as the database system. At Analytics, surround the ClickHouse repository calls through row consumption or `batch.Send()`. These durations include driver and network time. The stock PostgreSQL deployment supplies no per-request OTLP server spans. `pg_stat_statements` aggregates normalized statements. ClickHouse supports native trace context through `clickhouse.WithSpan` and can write server detail to `system.opentelemetry_span_log`. Publishing those spans to Jaeger would require a separate export and retention policy. The MVP delivers client spans for both databases and leaves engine profiling for a separate decision. [PostgreSQL statement statistics](https://www.postgresql.org/docs/18/pgstatstatements.html) · [ClickHouse driver](https://github.com/ClickHouse/clickhouse-go/blob/main/docs/config-reference.mdx) · [ClickHouse query tracing](https://clickhouse.com/blog/clickhouse-release-23-03)

Prefer driver-level hooks so ordinary database calls produce child spans from
the passed context. Wire the pgx pool tracer once during construction. Check
the native ClickHouse driver against both query and batch operation boundaries,
and use one injected adapter-level span where the driver does not emit a client
span covering the full operation. Keep database spans in the adapter layer.
Use separate middleware spans only for meaningful work such as device
credential verification when the HTTP span and database span leave a gap.
[pgx instrumentation](https://pkg.go.dev/github.com/exaring/otelpgx)

## Go tracer injection and span naming

Each Go service owns its SDK provider in application startup. Build tracers with
package import paths as instrumentation scopes. Pass `trace.Tracer` explicitly
through constructors into the use case, worker or adapter structs that start
spans, alongside their existing dependencies. Give middleware and client
instrumentation the local provider and a W3C `TraceContext` propagator explicitly.
Provide a no-op implementation at startup when tracing is disabled, and inject
an in-memory SDK provider in tests. Application code uses its injected tracer
throughout. [OpenTelemetry tracing API](https://opentelemetry.io/docs/specs/otel/trace/api/)

Use stable OpenTelemetry HTTP and database semantic conventions. HTTP server
spans use `METHOD /route/{parameter}` where a low cardinality route template is
available, and the method alone on an unmatched route. HTTP client spans use
the normalized method and standard attributes. Database client spans use
`db.system.name` and `db.operation.name`, with a fixed, safe `db.query.summary`
where it improves diagnosis. Keep query text and bind values out of spans.
[HTTP span conventions](https://opentelemetry.io/docs/specs/semconv/http/http-spans/) · [Database span conventions](https://opentelemetry.io/docs/specs/semconv/db/database-spans/)

Start one `INTERNAL` span at each public use case operation and pass its returned
context to nested work. Use fixed names of the form `domain.action`, with dotted
qualifiers for related operations. Examples include `auth.login`, `device.claim`,
`machine.baseline.set`, `telemetry.ingest`, `telemetry.detect`,
`telemetry.history.list`, `telemetry.batch.ingest`, and `alert.acknowledge`.
`outbox.deliver` is the worker attempt root. Span names contain only operation
names. End each span on all return paths, and represent unexpected failures
with a bounded error type and error status. Record expected business outcomes
such as a duplicate event replay with fixed values where they aid diagnosis.
Use standard HTTP status rules for HTTP spans.

## Crossing the outbox

The worker polls every second by default and can lease 1,000 unrelated rows. Add one nullable, fixed-length-bounded W3C `traceparent` column to `telemetry_outbox`. It includes the trace ID, ingestion span ID and flags. When inserting a new event, the repository writes the sampled local `telemetry.ingest` span context in the same transaction. Exact retries keep the original context. Keep this metadata out of the payload, event hash, Analytics wire contract and domain rules. Existing rows and older binaries can omit it.

After a nonempty claim, the worker starts a new root span named `outbox.deliver` for that attempt. Attach links to up to 32 distinct valid sampled producer contexts at span creation so the sampler can see them, and count excess links. The worker calls the delivery use case with the attempt context. That use case starts its own child span and carries it through send and acknowledge or retry. A retry creates a new attempt trace linked to the same producers. Missing or malformed context leaves the row deliverable. Empty polls produce no attempt spans. Keep claim and delivery logic in the use case layer, and separate the two phases so the worker can start its span with the claimed rows already known. Retain the existing sanitized logs for claim and prune failures.

## Operation on the Compose host

- Use private, internal trace-ingress networks for Nginx and the Go services separately. Attach the Collector to both and to `monitoring` for export to Jaeger. This keeps Nginx off the Go services' data network and away from Analytics. Keep PostgreSQL and ClickHouse on `data`. Use internal service DNS and port 4317 for OTLP. Publish only Jaeger's query UI/API on `127.0.0.1:16686`, reachable remotely via an SSH tunnel. Grafana can later query `http://jaeger:16686` on `monitoring`.
- The proposed trust boundary is one operator-managed host with trusted Compose members. OTLP uses plaintext inside those private segments. Trace components require no application, database or signing credentials. Use read-only mounted configuration. Moving export off-host would require TLS and dedicated exporter credentials supplied via secrets.
- Start at 1% sampling at Nginx and standalone Go roots. Child spans honor the trusted parent decision. Sample worker attempts carrying a sampled link so an accepted event can be followed across the queue, subject to the link cap and backend retention. Sample other attempts at 1%. Large mixed batches can therefore have a much higher effective sampling rate. Use 100% only for isolated verification. Head sampling can miss errors.
- Use a bounded, nonblocking batch processor in each Go process. Set a memory limiter, bounded batch size, bounded exporter queue and finite retry window in the Collector. Bound the Nginx per-worker queues too. Export failure may drop spans. Application startup, readiness, ingestion, delivery and database commits must continue through an exporter outage. Drain the Go providers with a short separate shutdown deadline after requests and workers finish.
- Use Jaeger all-in-one with embedded Badger on a dedicated volume and a proposed 24-hour TTL. Keep trace storage outside operational database backups. Budget disk separately and measure volume growth, container memory, API p95 and relay throughput on the seeded k6 workload with tracing enabled and disabled before choosing container limits for the host. [Jaeger single-node storage](https://www.jaegertracing.io/docs/2.23/storage/badger/)
- Allow only service identity, route template, normalized method, status, database system/operation, batch counts, timing and fixed error classes. Avoid raw URLs, cursors, request headers/bodies, tokens, email, machine/device/event identifiers, telemetry values, SQL, bind values and free-form exception strings. Apply controls in Go at the source. Apply a Collector allowlist before batching/export, including resource, span, event and link fields, and keep raw debug exporters disabled. Nginx default attributes can reach the trusted Collector before filtering.

## Verification and dependencies

1. In the two existing Go CI jobs, inject in-memory providers to verify distinct application instances, use case names and parent IDs on success and failure, route naming, sampling, redaction and shutdown. Check cancellation context reaches repositories and outbound calls. Exercise real PostgreSQL outbox rows with mixed, missing and invalid context, redelivery and capped links. Confirm hash/idempotency and the batch JSON contract.
2. Extend the deployment job's explicit Compose service/network/port/secret assertions. Validate pinned Collector and Jaeger configuration, module loading and both Nginx templates with `nginx -t`. Preserve the certificate and stack smoke tests.
3. In an isolated Compose run with full sampling, send one seeded device reading and one authorized history request through TLS Nginx. Poll the private Jaeger query API with a deadline. Assert service names, parent/child IDs, PostgreSQL and ClickHouse client spans, and the delivery link back to ingestion. Include canary sensitive fields and verify their absence from stored spans. Exercise spoofed external context and unsampled traffic. With the Collector unavailable, verify a successful ingest, export to Analytics and subsequent history read.

Prometheus currently self-scrapes. [Issue #17](https://github.com/vladgrskkh/machine-whisperer/issues/17) owns application metric endpoints and scrape connectivity. [Issue #18](https://github.com/vladgrskkh/machine-whisperer/issues/18) can add the Jaeger data source to Grafana once #10 supplies the query endpoint. Trace delivery works independently of both.

## Launch defaults

Capture database client latency first. Start with 24-hour retention and 1% root sampling. Measure trace storage growth and application overhead on the disposable workload before sizing the host deployment.
