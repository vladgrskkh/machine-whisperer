-- +goose Up
ALTER TABLE telemetry_outbox ADD COLUMN traceparent varchar(55);

-- +goose Down
ALTER TABLE telemetry_outbox DROP COLUMN traceparent;
