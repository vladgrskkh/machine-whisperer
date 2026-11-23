-- +goose Up
CREATE TABLE user_roles (
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role text NOT NULL CHECK (role = 'device_provisioner'),
    PRIMARY KEY (user_id, role)
);

-- +goose Down
DROP TABLE user_roles;
