# Grafana Dashboards Implementation Plan

Use Superpowers subagent driven development and independent task reviews.

**Goal** Ship versioned Grafana dashboards for the services and telemetry pipeline.

**Design** See [the approved dashboard design](../specs/2026-11-25-issue-18-grafana-dashboards-design.md).

## 1. Provision Grafana from Git

- [ ] Pin the image and provision the Prometheus data source and dashboard directory. Mount provisioned files read-only and use a stable data volume for operator settings.
- [ ] Route public HTTPS through Nginx at `/grafana/` on an isolated bridge. Require signed-in users and include named Viewer setup. Generate a dedicated initial admin secret and support adding it on existing deployments. Cover the volume in backup and restore.
- [ ] Document public sign-in, Viewer creation, secret rotation and the Prometheus retention controls.

## 2. Add the dashboard views

- [ ] Use the accepted #17 metric contract and stable job labels. Adapt compatible upstream Nginx, PostgreSQL, Go and ClickHouse panels and record their sources, revisions, licenses and modifications.
- [ ] Add the project-owned telemetry pipeline dashboard and links for each Go service. Use stable dashboard/data source UIDs and clearly render missing or stale series.
- [ ] Check expressions, data source health and dashboard UIDs against the real disposable stack with synthetic traffic and an outage/recovery case.

## Finish

- [ ] Resolve independent reviews and open the issue #18 PR after its metrics dependencies are available.
