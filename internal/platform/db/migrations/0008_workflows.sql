-- +goose Up
-- Workflow definitions and immutable versions (docs/workflows.md).
-- Versions are append-mostly: a published/deprecated version's spec
-- and hash never change; only drafts may be edited or deleted.

CREATE TABLE workflows (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  name text NOT NULL CHECK (name ~ '^[a-z0-9]([-a-z0-9]*[a-z0-9])?$'
                            AND length(name) <= 63),
  description text NOT NULL DEFAULT '',
  state text NOT NULL DEFAULT 'active' CHECK (state IN ('active','archived')),
  latest_published_version_id uuid,
  created_by uuid NOT NULL REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  version bigint NOT NULL DEFAULT 1,
  UNIQUE (project_id, name));

-- The workflow's tenant must own the project (defense in depth; the
-- service checks first).
-- +goose StatementBegin
CREATE FUNCTION workflow_requires_tenant_project() RETURNS trigger
  LANGUAGE plpgsql AS $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM projects
                 WHERE id = NEW.project_id
                   AND tenant_id = NEW.tenant_id) THEN
    RAISE EXCEPTION 'project does not belong to the workflow tenant';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER workflows_requires_tenant_project
  BEFORE INSERT OR UPDATE ON workflows
  FOR EACH ROW EXECUTE FUNCTION workflow_requires_tenant_project();
-- +goose StatementEnd

CREATE TABLE workflow_versions (
  id uuid PRIMARY KEY,
  workflow_id uuid NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  number integer NOT NULL CHECK (number > 0),
  state text NOT NULL DEFAULT 'draft'
    CHECK (state IN ('draft','published','deprecated')),
  schema_version text NOT NULL,
  spec jsonb NOT NULL,
  spec_hash bytea NOT NULL CHECK (length(spec_hash) = 32),
  layout jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_by uuid NOT NULL REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  published_at timestamptz,
  version bigint NOT NULL DEFAULT 1,
  UNIQUE (workflow_id, number));
CREATE INDEX workflow_versions_tenant ON workflow_versions (tenant_id);

-- Published history is immutable: once a version leaves draft only
-- state (forward-only), layout, published_at and version may change;
-- the spec, hash, number and provenance are frozen. Drafts may be
-- edited freely; only drafts may be deleted.
-- +goose StatementBegin
CREATE FUNCTION workflow_version_immutable() RETURNS trigger
  LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'DELETE' THEN
    IF OLD.state <> 'draft' THEN
      RAISE EXCEPTION 'only draft workflow versions may be deleted';
    END IF;
    RETURN OLD;
  END IF;
  IF OLD.state <> 'draft' THEN
    IF NEW.state = 'draft' THEN
      RAISE EXCEPTION 'workflow version state cannot move backwards';
    END IF;
    IF OLD.state = 'deprecated' AND NEW.state <> 'deprecated' THEN
      RAISE EXCEPTION 'deprecated workflow versions cannot be republished';
    END IF;
    IF NEW.spec IS DISTINCT FROM OLD.spec
       OR NEW.spec_hash IS DISTINCT FROM OLD.spec_hash
       OR NEW.number IS DISTINCT FROM OLD.number
       OR NEW.workflow_id IS DISTINCT FROM OLD.workflow_id
       OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id
       OR NEW.schema_version IS DISTINCT FROM OLD.schema_version
       OR NEW.created_by IS DISTINCT FROM OLD.created_by
       OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
      RAISE EXCEPTION 'published workflow versions are immutable';
    END IF;
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER workflow_versions_immutable
  BEFORE UPDATE OR DELETE ON workflow_versions
  FOR EACH ROW EXECUTE FUNCTION workflow_version_immutable();
-- +goose StatementEnd

-- Validations may now be associated with a workflow version.
ALTER TABLE script_validations
  ADD CONSTRAINT script_validations_workflow_version_fk
  FOREIGN KEY (workflow_version_id) REFERENCES workflow_versions(id)
  ON DELETE SET NULL;

-- A validation is only current for the exact input context that
-- produced it (pipeline.InputHash: language, resources, env, software,
-- cluster); currency lookups match on it so draft edits invalidate.
ALTER TABLE script_validations
  ADD COLUMN input_hash bigint NOT NULL DEFAULT 0;
DROP INDEX script_validations_currency;
CREATE INDEX script_validations_currency
  ON script_validations (tenant_id, script_digest, policy_version, input_hash);

ALTER TABLE workflows ENABLE ROW LEVEL SECURITY;
ALTER TABLE workflows FORCE ROW LEVEL SECURITY;
CREATE POLICY workflows_scope ON workflows
  USING (current_setting('app.tenant_id', true) = '*'
         OR tenant_id::text = current_setting('app.tenant_id', true))
  WITH CHECK (current_setting('app.tenant_id', true) = '*'
              OR tenant_id::text = current_setting('app.tenant_id', true));

ALTER TABLE workflow_versions ENABLE ROW LEVEL SECURITY;
ALTER TABLE workflow_versions FORCE ROW LEVEL SECURITY;
CREATE POLICY workflow_versions_scope ON workflow_versions
  USING (current_setting('app.tenant_id', true) = '*'
         OR tenant_id::text = current_setting('app.tenant_id', true))
  WITH CHECK (current_setting('app.tenant_id', true) = '*'
              OR tenant_id::text = current_setting('app.tenant_id', true));

-- +goose Down
DROP INDEX script_validations_currency;
CREATE INDEX script_validations_currency
  ON script_validations (tenant_id, script_digest, policy_version);
ALTER TABLE script_validations DROP COLUMN input_hash;
ALTER TABLE script_validations
  DROP CONSTRAINT script_validations_workflow_version_fk;

DROP POLICY workflow_versions_scope ON workflow_versions;
ALTER TABLE workflow_versions NO FORCE ROW LEVEL SECURITY;
ALTER TABLE workflow_versions DISABLE ROW LEVEL SECURITY;
DROP TRIGGER workflow_versions_immutable ON workflow_versions;
DROP TABLE workflow_versions;

DROP POLICY workflows_scope ON workflows;
ALTER TABLE workflows NO FORCE ROW LEVEL SECURITY;
ALTER TABLE workflows DISABLE ROW LEVEL SECURITY;
DROP TRIGGER workflows_requires_tenant_project ON workflows;
DROP TABLE workflows;

DROP FUNCTION workflow_version_immutable();
DROP FUNCTION workflow_requires_tenant_project();
