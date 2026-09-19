-- +goose Up
-- Workflow executions and per-task executions (docs/workflows.md
-- §Execution state machines). Executions are append-mostly: the
-- frozen ExecutionSpec on a task never changes once written.

CREATE TABLE workflow_executions (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  workflow_id uuid NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
  workflow_version_id uuid NOT NULL REFERENCES workflow_versions(id),
  spec_hash bytea NOT NULL CHECK (length(spec_hash) = 32),
  parameters jsonb NOT NULL DEFAULT '{}'::jsonb,
  strategy text NOT NULL DEFAULT 'engine'
    CHECK (strategy IN ('auto','engine','native')),
  state text NOT NULL DEFAULT 'PENDING' CHECK (state IN
    ('PENDING','VALIDATING','QUEUED','RUNNING','SUCCEEDED','FAILED',
     'PARTIAL_FAILURE','CANCELING','CANCELED')),
  state_reason text,
  requested_by uuid NOT NULL REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  started_at timestamptz,
  ended_at timestamptz,
  updated_at timestamptz NOT NULL DEFAULT now(),
  version bigint NOT NULL DEFAULT 1);
CREATE INDEX workflow_executions_tenant ON workflow_executions
  (tenant_id, created_at DESC, id);
CREATE INDEX workflow_executions_workflow ON workflow_executions
  (workflow_id, created_at DESC, id);
CREATE INDEX workflow_executions_active ON workflow_executions (tenant_id)
  WHERE state IN ('PENDING','VALIDATING','QUEUED','RUNNING','CANCELING');

-- The execution's tenant must own the version (defense in depth; the
-- service checks first).
-- +goose StatementBegin
CREATE FUNCTION execution_requires_tenant_version() RETURNS trigger
  LANGUAGE plpgsql AS $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM workflow_versions
                 WHERE id = NEW.workflow_version_id
                   AND tenant_id = NEW.tenant_id) THEN
    RAISE EXCEPTION 'workflow version does not belong to the execution tenant';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER workflow_executions_requires_tenant_version
  BEFORE INSERT OR UPDATE ON workflow_executions
  FOR EACH ROW EXECUTE FUNCTION execution_requires_tenant_version();
-- +goose StatementEnd

CREATE TABLE task_executions (
  id uuid PRIMARY KEY,
  execution_id uuid NOT NULL REFERENCES workflow_executions(id)
    ON DELETE CASCADE,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  task_name text NOT NULL,
  index integer NOT NULL DEFAULT 0,
  count integer NOT NULL DEFAULT 1,
  attempt integer NOT NULL DEFAULT 1,
  state text NOT NULL DEFAULT 'PENDING' CHECK (state IN
    ('PENDING','BLOCKED','READY','ADMITTING','SUBMITTING','QUEUED',
     'RUNNING','COMPLETED','FAILED','SKIPPED','CANCELED')),
  state_reason text,
  job_id uuid REFERENCES jobs(id),
  execution_spec jsonb,
  execution_spec_digest bytea,
  script_validation_id uuid REFERENCES script_validations(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  version bigint NOT NULL DEFAULT 1,
  UNIQUE (execution_id, task_name, index));
CREATE INDEX task_executions_execution ON task_executions
  (execution_id, task_name, index);
CREATE INDEX task_executions_active ON task_executions (execution_id)
  WHERE state NOT IN ('COMPLETED','FAILED','SKIPPED','CANCELED');

-- The frozen ExecutionSpec is written once: once non-null it never
-- changes (retry mints a new attempt before re-freezing).
-- +goose StatementBegin
CREATE FUNCTION task_executions_spec_immutable() RETURNS trigger
  LANGUAGE plpgsql AS $$
BEGIN
  -- A new attempt may clear the frozen spec for re-admission.
  IF NEW.attempt IS NOT DISTINCT FROM OLD.attempt THEN
    IF OLD.execution_spec IS NOT NULL
       AND NEW.execution_spec IS DISTINCT FROM OLD.execution_spec THEN
      RAISE EXCEPTION 'execution_spec is immutable';
    END IF;
    IF OLD.execution_spec_digest IS NOT NULL
       AND NEW.execution_spec_digest IS DISTINCT FROM OLD.execution_spec_digest THEN
      RAISE EXCEPTION 'execution_spec_digest is immutable';
    END IF;
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER task_executions_spec_no_update
  BEFORE UPDATE ON task_executions
  FOR EACH ROW EXECUTE FUNCTION task_executions_spec_immutable();
-- +goose StatementEnd

ALTER TABLE workflow_executions ENABLE ROW LEVEL SECURITY;
ALTER TABLE workflow_executions FORCE ROW LEVEL SECURITY;
CREATE POLICY workflow_executions_scope ON workflow_executions
  USING (current_setting('app.tenant_id', true) = '*'
         OR tenant_id::text = current_setting('app.tenant_id', true))
  WITH CHECK (current_setting('app.tenant_id', true) = '*'
              OR tenant_id::text = current_setting('app.tenant_id', true));

ALTER TABLE task_executions ENABLE ROW LEVEL SECURITY;
ALTER TABLE task_executions FORCE ROW LEVEL SECURITY;
CREATE POLICY task_executions_scope ON task_executions
  USING (current_setting('app.tenant_id', true) = '*'
         OR tenant_id::text = current_setting('app.tenant_id', true))
  WITH CHECK (current_setting('app.tenant_id', true) = '*'
              OR tenant_id::text = current_setting('app.tenant_id', true));

-- Command tasks have no script payload; workflow tasks link back to
-- their task execution for the reconcile → advance hook.
ALTER TABLE jobs ALTER COLUMN script_digest DROP NOT NULL;
ALTER TABLE jobs DROP CONSTRAINT IF EXISTS jobs_script_digest_check;
ALTER TABLE jobs ADD CONSTRAINT jobs_script_digest_check
  CHECK (script_digest IS NULL OR octet_length(script_digest) = 32);
ALTER TABLE jobs ADD COLUMN task_execution_id uuid
  REFERENCES task_executions(id);
CREATE INDEX jobs_task_execution ON jobs (task_execution_id)
  WHERE task_execution_id IS NOT NULL;

-- +goose Down
DROP INDEX jobs_task_execution;
ALTER TABLE jobs DROP COLUMN task_execution_id;
ALTER TABLE jobs DROP CONSTRAINT jobs_script_digest_check;
ALTER TABLE jobs ALTER COLUMN script_digest SET NOT NULL;
ALTER TABLE jobs ADD CONSTRAINT jobs_script_digest_check
  CHECK (octet_length(script_digest) = 32);

DROP POLICY task_executions_scope ON task_executions;
ALTER TABLE task_executions NO FORCE ROW LEVEL SECURITY;
ALTER TABLE task_executions DISABLE ROW LEVEL SECURITY;
DROP POLICY workflow_executions_scope ON workflow_executions;
ALTER TABLE workflow_executions NO FORCE ROW LEVEL SECURITY;
ALTER TABLE workflow_executions DISABLE ROW LEVEL SECURITY;

DROP TRIGGER task_executions_spec_no_update ON task_executions;
DROP FUNCTION task_executions_spec_immutable();
DROP TRIGGER workflow_executions_requires_tenant_version
  ON workflow_executions;
DROP FUNCTION execution_requires_tenant_version();
DROP TABLE task_executions;
DROP TABLE workflow_executions;
