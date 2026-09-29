-- +goose Up
ALTER TABLE clusters
  ADD COLUMN container_runtime jsonb
    CHECK (container_runtime IS NULL OR jsonb_typeof(container_runtime) = 'object');

-- +goose Down
ALTER TABLE clusters DROP COLUMN container_runtime;
