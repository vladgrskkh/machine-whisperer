# Machine Whisperer services

The [API](api/README.md) accepts device telemetry and commits each observation
to a PostgreSQL outbox. A worker in the API process sends pending batches to the
[analytics service](analytics/README.md), which stores telemetry history in
ClickHouse. Each service owns its Go module and tooling.
