CREATE TABLE IF NOT EXISTS telemetry_events
(
    schema_version UInt8,
    event_id UUID,
    device_id UUID,
    machine_id UUID,
    observed_at DateTime64(6, 'UTC'),
    received_at DateTime64(6, 'UTC'),
    operating_state LowCardinality(String),
    vibration_rms_g Float64,
    dominant_frequency_hz Nullable(Float64),
    current_rms_a Nullable(Float64)
)
ENGINE = ReplacingMergeTree(received_at)
PARTITION BY toYYYYMM(observed_at)
ORDER BY (machine_id, observed_at, device_id, event_id)
