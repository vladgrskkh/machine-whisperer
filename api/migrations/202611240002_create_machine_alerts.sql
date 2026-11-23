-- +goose Up
CREATE TABLE machine_detector_state (
    machine_id uuid PRIMARY KEY REFERENCES machines(id) ON DELETE CASCADE,
    baseline_version bigint NOT NULL CHECK (baseline_version > 0),
    pending_alert_count bigint NOT NULL CHECK (pending_alert_count >= 0),
    pending_recovery_count bigint NOT NULL CHECK (pending_recovery_count >= 0)
);

CREATE TABLE machine_alerts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    machine_id uuid NOT NULL REFERENCES machines(id) ON DELETE CASCADE,
    trigger_device_id uuid NOT NULL,
    trigger_event_id uuid NOT NULL,
    triggered_at timestamptz NOT NULL,
    trigger_z_score double precision NOT NULL CHECK (trigger_z_score >= 0),
    trigger_baseline_version bigint NOT NULL CHECK (trigger_baseline_version > 0),
    trigger_mean_rms_g double precision NOT NULL CHECK (
        trigger_mean_rms_g >= 0 AND trigger_mean_rms_g < 'Infinity'::double precision
    ),
    trigger_stddev_rms_g double precision NOT NULL CHECK (
        trigger_stddev_rms_g > 0 AND trigger_stddev_rms_g < 'Infinity'::double precision
    ),
    last_z_score double precision NOT NULL CHECK (last_z_score >= 0),
    peak_z_score double precision NOT NULL CHECK (
        peak_z_score >= greatest(trigger_z_score, last_z_score) AND peak_z_score < 'Infinity'::double precision
    ),
    recovery_device_id uuid,
    recovery_event_id uuid,
    recovered_at timestamptz CHECK (recovered_at >= triggered_at),
    recovery_baseline_version bigint CHECK (recovery_baseline_version >= trigger_baseline_version),
    acknowledged_at timestamptz,
    CHECK (num_nonnulls(recovery_device_id, recovery_event_id, recovered_at, recovery_baseline_version) IN (0, 4))
);

CREATE UNIQUE INDEX machine_alerts_one_active_idx ON machine_alerts (machine_id) WHERE recovered_at IS NULL;
CREATE INDEX machine_alerts_history_idx ON machine_alerts (machine_id, triggered_at DESC, id DESC);

-- +goose Down
DROP TABLE machine_alerts;
DROP TABLE machine_detector_state;
