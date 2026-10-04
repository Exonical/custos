-- +goose Up
ALTER TABLE workflow_executions
  ADD COLUMN is_test boolean NOT NULL DEFAULT false;

CREATE INDEX workflow_executions_active_test_version
  ON workflow_executions (workflow_version_id, created_at, id)
  WHERE is_test AND state IN ('PENDING','VALIDATING','QUEUED','RUNNING','CANCELING');

-- +goose Down
DROP INDEX workflow_executions_active_test_version;
ALTER TABLE workflow_executions DROP COLUMN is_test;
