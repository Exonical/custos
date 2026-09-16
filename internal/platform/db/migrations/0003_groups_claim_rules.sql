-- +goose Up
-- Groups, group memberships, and IdP claim-mapping rules (docs/tenancy.md,
-- docs/authentication.md JIT provisioning). All tenant-owned tables get the
-- same RLS shape as tenant_memberships: rows visible/writable under
-- app.tenant_id = '*' (platform scope) or the matching tenant uuid.
-- +goose StatementBegin
CREATE TABLE groups (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  name text NOT NULL CHECK (name ~ '^[A-Za-z0-9][A-Za-z0-9 ._-]{0,62}$'),
  description text NOT NULL DEFAULT '',
  source text NOT NULL CHECK (source IN ('manual','idp')),
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, name));
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TABLE group_memberships (
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  group_id uuid NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  source text NOT NULL CHECK (source IN ('manual','idp')),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (group_id, user_id));
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX group_memberships_user ON group_memberships (user_id);
-- +goose StatementEnd

-- A group member must be a tenant member. The service enforces this for
-- manual writes; the trigger is defense in depth (it runs as the invoking
-- role, so under tenant scope the membership row is visible to it).
-- +goose StatementBegin
CREATE FUNCTION group_member_requires_tenant_member() RETURNS trigger
  LANGUAGE plpgsql AS $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM tenant_memberships
                 WHERE tenant_id = NEW.tenant_id AND user_id = NEW.user_id) THEN
    RAISE EXCEPTION 'group member must be a tenant member';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER group_memberships_requires_tenant_member
  BEFORE INSERT OR UPDATE ON group_memberships
  FOR EACH ROW EXECUTE FUNCTION group_member_requires_tenant_member();
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE claim_mapping_rules (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  claim text NOT NULL CHECK (claim ~ '^[a-zA-Z_][a-zA-Z0-9_.:/-]{0,127}$'),
  match_value text NOT NULL CHECK (length(match_value) BETWEEN 1 AND 512),
  roles text[] NOT NULL CHECK (cardinality(roles) > 0),
  group_id uuid REFERENCES groups(id) ON DELETE SET NULL,
  enabled boolean NOT NULL DEFAULT true,
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, claim, match_value));
-- +goose StatementEnd
-- +goose StatementBegin
CREATE INDEX claim_mapping_rules_lookup ON claim_mapping_rules (claim, match_value) WHERE enabled;
-- +goose StatementEnd

-- Claims snapshot used to throttle IdP reconciliation in Provision.
-- +goose StatementBegin
ALTER TABLE users ADD COLUMN claims_hash bytea,
                  ADD COLUMN claims_synced_at timestamptz;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE groups ENABLE ROW LEVEL SECURITY;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE groups FORCE ROW LEVEL SECURITY;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE POLICY groups_scope ON groups
  USING (current_setting('app.tenant_id', true) = '*'
         OR tenant_id::text = current_setting('app.tenant_id', true))
  WITH CHECK (current_setting('app.tenant_id', true) = '*'
              OR tenant_id::text = current_setting('app.tenant_id', true));
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE group_memberships ENABLE ROW LEVEL SECURITY;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE group_memberships FORCE ROW LEVEL SECURITY;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE POLICY group_memberships_scope ON group_memberships
  USING (current_setting('app.tenant_id', true) = '*'
         OR tenant_id::text = current_setting('app.tenant_id', true))
  WITH CHECK (current_setting('app.tenant_id', true) = '*'
              OR tenant_id::text = current_setting('app.tenant_id', true));
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE claim_mapping_rules ENABLE ROW LEVEL SECURITY;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE claim_mapping_rules FORCE ROW LEVEL SECURITY;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE POLICY claim_mapping_rules_scope ON claim_mapping_rules
  USING (current_setting('app.tenant_id', true) = '*'
         OR tenant_id::text = current_setting('app.tenant_id', true))
  WITH CHECK (current_setting('app.tenant_id', true) = '*'
              OR tenant_id::text = current_setting('app.tenant_id', true));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP POLICY claim_mapping_rules_scope ON claim_mapping_rules;
-- +goose StatementEnd
-- +goose StatementBegin
DROP POLICY group_memberships_scope ON group_memberships;
-- +goose StatementEnd
-- +goose StatementBegin
DROP POLICY groups_scope ON groups;
-- +goose StatementEnd
-- +goose StatementBegin
ALTER TABLE users DROP COLUMN claims_hash, DROP COLUMN claims_synced_at;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE claim_mapping_rules;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TRIGGER group_memberships_requires_tenant_member ON group_memberships;
-- +goose StatementEnd
-- +goose StatementBegin
DROP FUNCTION group_member_requires_tenant_member();
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE group_memberships;
-- +goose StatementEnd
-- +goose StatementBegin
DROP TABLE groups;
-- +goose StatementEnd
