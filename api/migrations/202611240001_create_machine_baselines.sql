-- +goose Up
CREATE TABLE machine_baselines (
    machine_id uuid PRIMARY KEY REFERENCES machines(id) ON DELETE CASCADE,
    mean_rms_g double precision NOT NULL CHECK (mean_rms_g >= 0 AND mean_rms_g < 'Infinity'::double precision),
    stddev_rms_g double precision NOT NULL CHECK (stddev_rms_g > 0 AND stddev_rms_g < 'Infinity'::double precision),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);

-- +goose Down
DROP TABLE machine_baselines;
