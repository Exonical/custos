-- +goose Up
ALTER TABLE clusters
  ADD COLUMN software_modules jsonb NOT NULL DEFAULT '[]'::jsonb
    CHECK (jsonb_typeof(software_modules) = 'array');

-- +goose Down
ALTER TABLE clusters DROP COLUMN software_modules;
