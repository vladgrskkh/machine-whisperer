-- +goose Up
CREATE TABLE sessions (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz
);

CREATE INDEX sessions_user_id_idx ON sessions (user_id);

CREATE TABLE refresh_tokens (
    hash bytea PRIMARY KEY CHECK (octet_length(hash) = 32),
    session_id uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL,
    consumed_at timestamptz
);

CREATE INDEX refresh_tokens_session_id_idx ON refresh_tokens (session_id);

-- +goose Down
DROP TABLE refresh_tokens;
DROP TABLE sessions;
