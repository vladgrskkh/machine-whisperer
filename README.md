# Machine Whisperer services

[![API coverage](https://img.shields.io/endpoint?url=https%3A%2F%2Fraw.githubusercontent.com%2Fvladgrskkh%2Fmachine-whisperer%2Fcoverage-badges%2Fapi.json)](https://github.com/vladgrskkh/machine-whisperer/actions/workflows/ci.yml)
[![Analytics coverage](https://img.shields.io/endpoint?url=https%3A%2F%2Fraw.githubusercontent.com%2Fvladgrskkh%2Fmachine-whisperer%2Fcoverage-badges%2Fanalytics.json)](https://github.com/vladgrskkh/machine-whisperer/actions/workflows/ci.yml)

The [API](api/README.md) accepts device telemetry and commits each observation
to a PostgreSQL outbox. A worker in the API process sends pending batches to the
[analytics service](analytics/README.md), which stores telemetry history in
ClickHouse. Each service owns its Go module and tooling.
