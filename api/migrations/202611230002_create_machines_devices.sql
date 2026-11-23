-- +goose Up
CREATE TABLE machines (
    id uuid PRIMARY KEY,
    owner_user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name text NOT NULL,
    created_at timestamptz NOT NULL
);

CREATE INDEX machines_owner_created_idx ON machines (owner_user_id, created_at DESC, id DESC);

CREATE TABLE devices (
    id uuid PRIMARY KEY,
    machine_id uuid REFERENCES machines(id) ON DELETE CASCADE,
    key_verifier bytea NOT NULL UNIQUE CHECK (octet_length(key_verifier) = 32),
    claim_verifier bytea UNIQUE CHECK (octet_length(claim_verifier) = 32),
    claim_expires_at timestamptz NOT NULL,
    claimed_at timestamptz,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL,
    CONSTRAINT devices_claim_state CHECK (
        (machine_id IS NULL AND claimed_at IS NULL AND claim_verifier IS NOT NULL) OR
        (machine_id IS NOT NULL AND claimed_at IS NOT NULL AND claim_verifier IS NULL)
    )
);

CREATE INDEX devices_machine_idx ON devices (machine_id);
CREATE INDEX devices_claim_expiry_idx ON devices (claim_expires_at) WHERE claim_verifier IS NOT NULL;

-- +goose Down
DROP TABLE devices;
DROP TABLE machines;
