-- +goose Up
-- Validation policies and persisted ScriptValidation results
-- (docs/script-validation.md §Persistence). Cluster-scope policy rows
-- are visible to every tenant (they merge into the effective policy);
-- tenant-scope rows are RLS-scoped. script_validations is fully
-- tenant-scoped. Jobs gain the validation that admitted them.

CREATE TABLE validation_policies (
  id uuid PRIMARY KEY,
  scope_kind text NOT NULL CHECK (scope_kind IN ('tenant','cluster')),
  scope_id uuid NOT NULL,
  version bigint NOT NULL,
  body jsonb NOT NULL,
  updated_by uuid REFERENCES users(id),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (scope_kind, scope_id));

ALTER TABLE validation_policies ENABLE ROW LEVEL SECURITY;
ALTER TABLE validation_policies FORCE ROW LEVEL SECURITY;
CREATE POLICY validation_policies_scope ON validation_policies
  USING (current_setting('app.tenant_id', true) = '*'
         OR scope_kind = 'cluster'
         OR scope_id::text = current_setting('app.tenant_id', true))
  -- Tenants may READ cluster rows (USING above) but only the platform
  -- scope may WRITE them; a tenant session can only write its own
  -- tenant-scope row.
  WITH CHECK (current_setting('app.tenant_id', true) = '*'
              OR (scope_kind = 'tenant'
                  AND scope_id::text = current_setting('app.tenant_id', true)));

CREATE TABLE script_validations (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  workflow_version_id uuid,
  task_name text NOT NULL DEFAULT '',
  script_digest bytea NOT NULL CHECK (octet_length(script_digest) = 32),
  language text NOT NULL,
  valid boolean NOT NULL,
  diagnostics jsonb NOT NULL DEFAULT '[]'::jsonb,
  tool_versions jsonb NOT NULL DEFAULT '{}'::jsonb,
  policy_version bigint NOT NULL,
  validated_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL);
CREATE INDEX script_validations_currency
  ON script_validations (tenant_id, script_digest, policy_version);
CREATE INDEX script_validations_wfv
  ON script_validations (workflow_version_id)
  WHERE workflow_version_id IS NOT NULL;

ALTER TABLE script_validations ENABLE ROW LEVEL SECURITY;
ALTER TABLE script_validations FORCE ROW LEVEL SECURITY;
CREATE POLICY script_validations_scope ON script_validations
  USING (current_setting('app.tenant_id', true) = '*'
         OR tenant_id::text = current_setting('app.tenant_id', true))
  WITH CHECK (current_setting('app.tenant_id', true) = '*'
              OR tenant_id::text = current_setting('app.tenant_id', true));

ALTER TABLE jobs ADD COLUMN script_validation_id uuid;

-- +goose Down
ALTER TABLE jobs DROP COLUMN script_validation_id;

DROP POLICY script_validations_scope ON script_validations;
ALTER TABLE script_validations NO FORCE ROW LEVEL SECURITY;
ALTER TABLE script_validations DISABLE ROW LEVEL SECURITY;
DROP TABLE script_validations;

DROP POLICY validation_policies_scope ON validation_policies;
ALTER TABLE validation_policies NO FORCE ROW LEVEL SECURITY;
ALTER TABLE validation_policies DISABLE ROW LEVEL SECURITY;
DROP TABLE validation_policies;
