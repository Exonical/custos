-- +goose Up
-- Tenant-scoped secret connectors and references. Values and connector
-- credentials are never stored in PostgreSQL (docs/secrets.md).

CREATE TABLE secret_connectors (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  name text NOT NULL CHECK (name ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$'),
  kind text NOT NULL CHECK (kind IN ('platform-openbao','openbao')),
  state text NOT NULL CHECK (state IN ('active','disabled')),
  config jsonb NOT NULL DEFAULT '{}'::jsonb,
  credential_ref jsonb,
  created_by uuid NOT NULL REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  version bigint NOT NULL DEFAULT 1,
  UNIQUE (tenant_id, name));

CREATE TABLE secret_references (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  owner_id uuid REFERENCES users(id) ON DELETE RESTRICT,
  project_id uuid REFERENCES projects(id) ON DELETE CASCADE,
  name text NOT NULL CHECK (name ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$'),
  connector_id uuid NOT NULL REFERENCES secret_connectors(id) ON DELETE RESTRICT,
  namespace text NOT NULL,
  mount text NOT NULL,
  path text NOT NULL,
  key text NOT NULL,
  secret_version integer CHECK (secret_version IS NULL OR secret_version > 0),
  kind text NOT NULL CHECK (kind IN
    ('ssh_key','slurm_token','api_token','generic','storage_credential')),
  allowed_uses text[] NOT NULL DEFAULT '{}',
  created_by uuid NOT NULL REFERENCES users(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  version bigint NOT NULL DEFAULT 1,
  UNIQUE (tenant_id, name));
CREATE INDEX secret_references_connector ON secret_references (connector_id);
CREATE INDEX secret_references_owner ON secret_references (tenant_id, owner_id);

-- +goose StatementBegin
CREATE FUNCTION secret_reference_project_matches_tenant() RETURNS trigger
  LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.project_id IS NOT NULL AND NOT EXISTS (
      SELECT 1 FROM projects WHERE id = NEW.project_id
      AND tenant_id = NEW.tenant_id) THEN
    RAISE EXCEPTION 'secret reference project does not belong to tenant';
  END IF;
  IF NOT EXISTS (SELECT 1 FROM secret_connectors
                 WHERE id = NEW.connector_id AND tenant_id = NEW.tenant_id) THEN
    RAISE EXCEPTION 'secret connector does not belong to tenant';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER secret_references_tenant_integrity
  BEFORE INSERT OR UPDATE ON secret_references
  FOR EACH ROW EXECUTE FUNCTION secret_reference_project_matches_tenant();

ALTER TABLE secret_connectors ENABLE ROW LEVEL SECURITY;
ALTER TABLE secret_connectors FORCE ROW LEVEL SECURITY;
CREATE POLICY secret_connectors_scope ON secret_connectors
  USING (current_setting('app.tenant_id', true) = '*'
         OR tenant_id::text = current_setting('app.tenant_id', true))
  WITH CHECK (current_setting('app.tenant_id', true) = '*'
              OR tenant_id::text = current_setting('app.tenant_id', true));

ALTER TABLE secret_references ENABLE ROW LEVEL SECURITY;
ALTER TABLE secret_references FORCE ROW LEVEL SECURITY;
CREATE POLICY secret_references_scope ON secret_references
  USING (current_setting('app.tenant_id', true) = '*'
         OR tenant_id::text = current_setting('app.tenant_id', true))
  WITH CHECK (current_setting('app.tenant_id', true) = '*'
              OR tenant_id::text = current_setting('app.tenant_id', true));

-- +goose Down
DROP POLICY secret_references_scope ON secret_references;
ALTER TABLE secret_references NO FORCE ROW LEVEL SECURITY;
DROP TRIGGER secret_references_tenant_integrity ON secret_references;
DROP TABLE secret_references;
DROP FUNCTION secret_reference_project_matches_tenant();
DROP POLICY secret_connectors_scope ON secret_connectors;
ALTER TABLE secret_connectors NO FORCE ROW LEVEL SECURITY;
DROP TABLE secret_connectors;
