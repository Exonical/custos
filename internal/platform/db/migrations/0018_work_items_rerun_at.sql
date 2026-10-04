-- +goose Up
ALTER TABLE work_items ADD COLUMN rerun_at timestamptz;

-- +goose Down
ALTER TABLE work_items DROP COLUMN rerun_at;
