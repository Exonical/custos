-- +goose Up
CREATE TABLE clusters (
  id uuid PRIMARY KEY,
  name text NOT NULL UNIQUE CHECK (name ~ '^[a-z0-9][a-z0-9-]{1,62}$'),
  display_name text NOT NULL,
  base_url text NOT NULL,
  api_version text NOT NULL CHECK (api_version IN ('v0.0.45','v0.0.44')),
  ca_bundle_pem text,
  identity_mode text NOT NULL CHECK (identity_mode IN ('service','impersonate')),
  service_user text NOT NULL,
  token_ref jsonb NOT NULL,          -- secrets.Reference; a reference, never a value
  client_cert_ref jsonb,
  visibility text NOT NULL CHECK (visibility IN ('assigned','all_tenants')),
  state text NOT NULL CHECK (state IN ('active','degraded','unreachable','disabled')),
  consecutive_failures integer NOT NULL DEFAULT 0,
  consecutive_successes integer NOT NULL DEFAULT 0,
  last_sync_at timestamptz, last_error text,
  capabilities jsonb, capabilities_at timestamptz,
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE cluster_tenant_assignments (
  cluster_id uuid NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  source text NOT NULL CHECK (source IN ('manual','auto')),
  defaults jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (cluster_id, tenant_id));
CREATE INDEX cluster_tenant_assignments_tenant ON cluster_tenant_assignments (tenant_id);
ALTER TABLE cluster_tenant_assignments ENABLE ROW LEVEL SECURITY;
ALTER TABLE cluster_tenant_assignments FORCE ROW LEVEL SECURITY;
CREATE POLICY cluster_tenant_assignments_scope ON cluster_tenant_assignments
  USING (current_setting('app.tenant_id', true) = '*'
         OR tenant_id::text = current_setting('app.tenant_id', true))
  WITH CHECK (current_setting('app.tenant_id', true) = '*'
              OR tenant_id::text = current_setting('app.tenant_id', true));
CREATE TABLE cluster_partitions (
  cluster_id uuid NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
  name text NOT NULL, attributes jsonb NOT NULL, synced_at timestamptz NOT NULL,
  PRIMARY KEY (cluster_id, name));

-- +goose Down
DROP TABLE cluster_partitions;
DROP TABLE cluster_tenant_assignments;
DROP TABLE clusters;
