-- +goose Up
ALTER TABLE machines ADD COLUMN deleted_at timestamptz;

-- +goose Down
ALTER TABLE machines DROP COLUMN deleted_at;
