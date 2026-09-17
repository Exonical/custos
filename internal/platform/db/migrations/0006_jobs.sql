-- +goose Up
-- Scripts (content-addressed, immutable) and jobs (docs/workers.md,
-- docs/script-validation.md). Both tables are RLS-forced with the
-- '*'/tenant_id scope policy.

CREATE TABLE scripts (
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  sha256 bytea NOT NULL CHECK (octet_length(sha256) = 32),
  language text NOT NULL,
  size integer NOT NULL,
  body bytea NOT NULL,
  created_by uuid REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, sha256));

-- Scripts are content-addressed and immutable (append-only like
-- audit_events; GC is a later maintenance item).
-- +goose StatementBegin
CREATE FUNCTION scripts_immutable() RETURNS trigger
  LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'scripts are immutable';
END;
$$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER scripts_no_update
  BEFORE UPDATE OR DELETE ON scripts
  FOR EACH ROW EXECUTE FUNCTION scripts_immutable();
-- +goose StatementEnd

CREATE TABLE jobs (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  cluster_id uuid NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
  created_by uuid NOT NULL REFERENCES users(id),
  name text NOT NULL,
  state text NOT NULL CHECK (state IN
    ('SUBMITTING','QUEUED','RUNNING','COMPLETED','FAILED','CANCELED')),
  state_reason text,
  slurm_job_id bigint,
  slurm_state text,
  exit_code integer,
  exit_signal integer,
  resource_request jsonb NOT NULL,
  execution_spec jsonb NOT NULL,
  execution_spec_digest bytea NOT NULL CHECK (octet_length(execution_spec_digest) = 32),
  script_digest bytea NOT NULL CHECK (octet_length(script_digest) = 32),
  script_language text NOT NULL,
  submitted_at timestamptz,
  started_at timestamptz,
  ended_at timestamptz,
  last_reconciled_at timestamptz,
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now());

CREATE UNIQUE INDEX jobs_slurm ON jobs (cluster_id, slurm_job_id)
  WHERE slurm_job_id IS NOT NULL;
CREATE INDEX jobs_tenant_time ON jobs (tenant_id, created_at DESC, id);
CREATE INDEX jobs_project_time ON jobs (tenant_id, project_id, created_at DESC, id);
CREATE INDEX jobs_active ON jobs (cluster_id)
  WHERE state IN ('SUBMITTING','QUEUED','RUNNING');

-- The execution spec and its digests are written once and never
-- updated: they are the canonical record of intent.
-- +goose StatementBegin
CREATE FUNCTION jobs_spec_immutable() RETURNS trigger
  LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.execution_spec IS DISTINCT FROM OLD.execution_spec
     OR NEW.execution_spec_digest IS DISTINCT FROM OLD.execution_spec_digest
     OR NEW.script_digest IS DISTINCT FROM OLD.script_digest THEN
    RAISE EXCEPTION 'execution_spec is immutable';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER jobs_spec_no_update
  BEFORE UPDATE ON jobs
  FOR EACH ROW EXECUTE FUNCTION jobs_spec_immutable();
-- +goose StatementEnd

ALTER TABLE scripts ENABLE ROW LEVEL SECURITY;
ALTER TABLE scripts FORCE ROW LEVEL SECURITY;
CREATE POLICY scripts_scope ON scripts
  USING (current_setting('app.tenant_id', true) = '*'
         OR tenant_id::text = current_setting('app.tenant_id', true))
  WITH CHECK (current_setting('app.tenant_id', true) = '*'
              OR tenant_id::text = current_setting('app.tenant_id', true));

ALTER TABLE jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE jobs FORCE ROW LEVEL SECURITY;
CREATE POLICY jobs_scope ON jobs
  USING (current_setting('app.tenant_id', true) = '*'
         OR tenant_id::text = current_setting('app.tenant_id', true))
  WITH CHECK (current_setting('app.tenant_id', true) = '*'
              OR tenant_id::text = current_setting('app.tenant_id', true));

-- +goose Down
DROP POLICY jobs_scope ON jobs;
ALTER TABLE jobs NO FORCE ROW LEVEL SECURITY;
ALTER TABLE jobs DISABLE ROW LEVEL SECURITY;
DROP TRIGGER jobs_spec_no_update ON jobs;
DROP TABLE jobs;
DROP FUNCTION jobs_spec_immutable();

DROP POLICY scripts_scope ON scripts;
ALTER TABLE scripts NO FORCE ROW LEVEL SECURITY;
ALTER TABLE scripts DISABLE ROW LEVEL SECURITY;
DROP TRIGGER scripts_no_update ON scripts;
DROP TABLE scripts;
DROP FUNCTION scripts_immutable();
