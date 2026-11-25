# Prometheus Metrics Implementation Plan

Use Superpowers subagent driven development and independent task reviews.

**Goal** Expose application, outbox and database signals to the existing Prometheus service.

**Design** See [the approved design](../specs/2026-11-25-issue-17-prometheus-metrics-design.md).

## 1. Instrument the API

- [ ] Add an application-owned Prometheus registry and a private metrics listener with process, Go and HTTP collectors. Name HTTP series using registered route templates.
- [ ] Count telemetry admission and replay, detector and alert transitions after commits, and relay outcomes using confirmed affected rows. Read one bounded outbox snapshot on each scrape.
- [ ] Check independent registries, status and route labels, rollbacks, repeated acknowledgements, unavailable snapshots and lost leases using the shared Testcontainer suite.

## 2. Instrument Analytics

- [ ] Add the same registry and listener lifecycle for Analytics. Measure batch dispositions, physically acknowledged event rows, and ClickHouse client operations. The native database scrape jobs supply connection metrics.
- [ ] Check real batch writes and history queries, failure classification and low cardinality using the existing ClickHouse integration fixture.

## 3. Wire the application scrape network

- [ ] Add the private scrape bridge to Compose, inject explicit listener addresses, and add the two application jobs. Keep the public ingress and database query ports restricted.
- [ ] Verify effective settings with promtool and test the actual exposition and target behavior on a disposable Compose stack.

## 4. Wire infrastructure targets

- [ ] Add OSS Nginx exporter with a private status listener, scoped postgres_exporter login with file-backed credentials, and ClickHouse native metrics behind a fixed-path proxy.
- [ ] Add infrastructure jobs on the private infrastructure scrape bridge. Verify representative series and upstream failures. Preserve certificate bootstrap behavior and existing Compose volumes during upgrades.
- [ ] Document metrics access, scrape targets and fresh/existing deployment setup. Update the project observability design to show direct pull and separate tracing.

## Finish

- [ ] Run independent reviews and verification, then open a PR for issue #17.
