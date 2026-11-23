package migrations

import _ "embed"

//go:embed 001_create_telemetry_events.sql
var CreateTelemetryEvents string
