-- +goose Up
ALTER TABLE machines ADD COLUMN last_telemetry_observed_at timestamptz;

CREATE TABLE telemetry_events (
    device_id uuid NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
    event_id uuid NOT NULL,
    machine_id uuid NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
    observed_at timestamptz NOT NULL,
    payload_hash bytea NOT NULL CHECK (octet_length(payload_hash) = 32),
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (device_id, event_id)
);

CREATE INDEX telemetry_events_expiry_idx ON telemetry_events (expires_at);
CREATE INDEX telemetry_events_machine_idx ON telemetry_events (machine_id);

CREATE TABLE telemetry_outbox (
    device_id uuid NOT NULL,
    event_id uuid NOT NULL,
    machine_id uuid NOT NULL,
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    delivered_at timestamptz,
    PRIMARY KEY (device_id, event_id)
);

-- +goose Down
DROP TABLE telemetry_outbox;
DROP TABLE telemetry_events;
ALTER TABLE machines DROP COLUMN last_telemetry_observed_at;
