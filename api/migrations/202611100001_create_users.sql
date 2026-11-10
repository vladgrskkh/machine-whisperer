-- +goose Up
CREATE TABLE users (
    id uuid PRIMARY KEY,
    email text NOT NULL CONSTRAINT users_email_key UNIQUE,
    password_hash text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT users_email_canonical CHECK (email = lower(btrim(email)) AND octet_length(email) <= 254)
);

-- +goose Down
DROP TABLE users;
