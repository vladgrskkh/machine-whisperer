# Distributed Tracing Implementation Plan

Use Superpowers subagent driven development and independent task reviews.

**Goal** Follow HTTPS requests and their asynchronous outbox deliveries across Nginx, API, Analytics and both database clients.

**Design** See [the approved tracing design](../specs/2026-11-25-issue-10-otel-tracing-design.md).

## 1. Instrument Go HTTP and application operations

- [ ] Construct an OpenTelemetry provider and W3C Trace Context propagator for each process. Pass them explicitly to HTTP instrumentation and inject scoped tracers into the structs that produce spans.
- [ ] Use otelhttp for both chi routers and the API's Analytics HTTP clients. Verify template based server names, standard HTTP attributes, trusted context and outgoing propagation.
- [ ] Create child spans in use case methods. Prefer injected PostgreSQL driver instrumentation and verify native ClickHouse tracing covers the full client operation. Add an adapter span where needed.
- [ ] Exercise parentage, sanitized attributes and exporter outage behavior with in-memory SDK providers and real database integration tests.

## 2. Correlate outbox delivery

- [ ] Store a nullable W3C traceparent for sampled accepted telemetry. Give the worker one root span per nonempty attempt and link it to the original ingestion spans before starting it.
- [ ] Pass the worker span context into the delivery use case. Ensure retries preserve the producer context, malformed metadata stays deliverable and exact replays leave the row unchanged.
- [ ] Check batches with valid, missing and malformed context, link caps, delivered rows and retries.

## 3. Connect Nginx, Collector and Jaeger

- [ ] Pin compatible images, preserve ingress trust and TLS behavior and configure the native Nginx OTel module.
- [ ] Use private OTLP segments, bounded processors and exporters, and private Jaeger access. Derive sampling and retention from the documented defaults and check resource cost on a disposable workload.
- [ ] Verify a sampled request and its linked delivery in Jaeger on the Compose stack. Check external trace header replacement, attribute filtering and ingest/history availability during Collector outage.

## Finish

- [ ] Resolve independent reviews and open the issue #10 PR.
