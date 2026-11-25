-- +goose Up
ALTER TABLE devices
    ADD COLUMN provisioned_by_user_id uuid REFERENCES users(id) ON DELETE SET NULL;

CREATE INDEX devices_provisioned_by_user_idx
    ON devices (provisioned_by_user_id) WHERE provisioned_by_user_id IS NOT NULL;

-- +goose Down
ALTER TABLE devices DROP COLUMN provisioned_by_user_id;
