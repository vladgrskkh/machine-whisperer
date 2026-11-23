-- +goose Up
ALTER TABLE telemetry_outbox
    ADD COLUMN available_at timestamptz NOT NULL DEFAULT now(),
    ADD COLUMN lease_token uuid,
    ADD COLUMN lease_until timestamptz,
    ADD COLUMN attempt_count bigint NOT NULL DEFAULT 0,
    ADD COLUMN last_error_category text,
    ADD CONSTRAINT telemetry_outbox_lease_pair CHECK ((lease_token IS NULL) = (lease_until IS NULL));

CREATE INDEX telemetry_outbox_due_idx ON telemetry_outbox (available_at, created_at, device_id, event_id)
    WHERE delivered_at IS NULL AND lease_until IS NULL;
CREATE INDEX telemetry_outbox_lease_expiry_idx ON telemetry_outbox (lease_until)
    WHERE delivered_at IS NULL AND lease_until IS NOT NULL;
CREATE INDEX telemetry_outbox_delivered_idx ON telemetry_outbox (delivered_at, device_id, event_id)
    WHERE delivered_at IS NOT NULL;

-- +goose Down
DROP INDEX telemetry_outbox_delivered_idx;
DROP INDEX telemetry_outbox_lease_expiry_idx;
DROP INDEX telemetry_outbox_due_idx;
ALTER TABLE telemetry_outbox
    DROP CONSTRAINT telemetry_outbox_lease_pair,
    DROP COLUMN last_error_category,
    DROP COLUMN attempt_count,
    DROP COLUMN lease_until,
    DROP COLUMN lease_token,
    DROP COLUMN available_at;
