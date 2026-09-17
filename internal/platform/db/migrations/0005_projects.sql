-- +goose Up
-- Projects, project memberships, project-cluster bindings and resource
-- policies (docs/tenancy.md). Every table is RLS-forced with the
-- '*'/tenant_id scope policy — app.tenant_id is '*' (platform scope) or
-- the matching tenant uuid.

CREATE TABLE projects (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  slug text NOT NULL CHECK (slug ~ '^[a-z0-9][a-z0-9-]{1,62}$'),
  name text NOT NULL,
  description text NOT NULL DEFAULT '',
  state text NOT NULL CHECK (state IN ('active','archived')),
  settings jsonb NOT NULL DEFAULT '{}'::jsonb,
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, slug));

CREATE TABLE project_memberships (
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  roles text[] NOT NULL CHECK (cardinality(roles) > 0),
  source text NOT NULL CHECK (source IN ('manual','idp')),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (project_id, user_id));
CREATE INDEX project_memberships_user ON project_memberships (user_id);

-- A project member must be a tenant member; same defense-in-depth
-- shape as group_memberships (the service checks first; the trigger
-- runs as the invoking role under the current RLS scope).
-- +goose StatementBegin
CREATE FUNCTION project_member_requires_tenant_member() RETURNS trigger
  LANGUAGE plpgsql AS $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM tenant_memberships
                 WHERE tenant_id = NEW.tenant_id AND user_id = NEW.user_id) THEN
    RAISE EXCEPTION 'project member must be a tenant member';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER project_memberships_requires_tenant_member
  BEFORE INSERT OR UPDATE ON project_memberships
  FOR EACH ROW EXECUTE FUNCTION project_member_requires_tenant_member();
-- +goose StatementEnd

CREATE TABLE project_cluster_bindings (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  cluster_id uuid NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
  slurm_account text NOT NULL CHECK (slurm_account ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$'),
  default_partition text NOT NULL DEFAULT '',
  allowed_partitions text[] NOT NULL DEFAULT '{}',
  default_qos text NOT NULL DEFAULT '',
  allowed_qos text[] NOT NULL DEFAULT '{}',
  enabled boolean NOT NULL DEFAULT true,
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (project_id, cluster_id));

-- A binding's cluster must be assigned to the tenant. The EXISTS check
-- on cluster_tenant_assignments runs under the current RLS scope —
-- under a tenant scope the row is only visible when assigned.
-- +goose StatementBegin
CREATE FUNCTION binding_requires_assigned_cluster() RETURNS trigger
  LANGUAGE plpgsql AS $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM cluster_tenant_assignments
                 WHERE cluster_id = NEW.cluster_id
                   AND tenant_id = NEW.tenant_id) THEN
    RAISE EXCEPTION 'cluster is not assigned to the tenant';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER project_cluster_bindings_requires_assigned_cluster
  BEFORE INSERT OR UPDATE ON project_cluster_bindings
  FOR EACH ROW EXECUTE FUNCTION binding_requires_assigned_cluster();
-- +goose StatementEnd

CREATE TABLE resource_policies (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  scope text NOT NULL CHECK (scope IN ('tenant','project')),
  project_id uuid REFERENCES projects(id) ON DELETE CASCADE,
  CHECK ((scope='tenant' AND project_id IS NULL)
         OR (scope='project' AND project_id IS NOT NULL)),
  policy jsonb NOT NULL,
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now());
CREATE UNIQUE INDEX resource_policies_tenant_one
  ON resource_policies (tenant_id) WHERE scope='tenant';
CREATE UNIQUE INDEX resource_policies_project_one
  ON resource_policies (project_id) WHERE scope='project';

ALTER TABLE projects ENABLE ROW LEVEL SECURITY;
ALTER TABLE projects FORCE ROW LEVEL SECURITY;
CREATE POLICY projects_scope ON projects
  USING (current_setting('app.tenant_id', true) = '*'
         OR tenant_id::text = current_setting('app.tenant_id', true))
  WITH CHECK (current_setting('app.tenant_id', true) = '*'
              OR tenant_id::text = current_setting('app.tenant_id', true));

ALTER TABLE project_memberships ENABLE ROW LEVEL SECURITY;
ALTER TABLE project_memberships FORCE ROW LEVEL SECURITY;
CREATE POLICY project_memberships_scope ON project_memberships
  USING (current_setting('app.tenant_id', true) = '*'
         OR tenant_id::text = current_setting('app.tenant_id', true))
  WITH CHECK (current_setting('app.tenant_id', true) = '*'
              OR tenant_id::text = current_setting('app.tenant_id', true));

ALTER TABLE project_cluster_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE project_cluster_bindings FORCE ROW LEVEL SECURITY;
CREATE POLICY project_cluster_bindings_scope ON project_cluster_bindings
  USING (current_setting('app.tenant_id', true) = '*'
         OR tenant_id::text = current_setting('app.tenant_id', true))
  WITH CHECK (current_setting('app.tenant_id', true) = '*'
              OR tenant_id::text = current_setting('app.tenant_id', true));

ALTER TABLE resource_policies ENABLE ROW LEVEL SECURITY;
ALTER TABLE resource_policies FORCE ROW LEVEL SECURITY;
CREATE POLICY resource_policies_scope ON resource_policies
  USING (current_setting('app.tenant_id', true) = '*'
         OR tenant_id::text = current_setting('app.tenant_id', true))
  WITH CHECK (current_setting('app.tenant_id', true) = '*'
              OR tenant_id::text = current_setting('app.tenant_id', true));

-- +goose Down
DROP POLICY resource_policies_scope ON resource_policies;
ALTER TABLE resource_policies NO FORCE ROW LEVEL SECURITY;
ALTER TABLE resource_policies DISABLE ROW LEVEL SECURITY;
DROP TABLE resource_policies;

DROP POLICY project_cluster_bindings_scope ON project_cluster_bindings;
ALTER TABLE project_cluster_bindings NO FORCE ROW LEVEL SECURITY;
ALTER TABLE project_cluster_bindings DISABLE ROW LEVEL SECURITY;
DROP TRIGGER project_cluster_bindings_requires_assigned_cluster ON project_cluster_bindings;
DROP TABLE project_cluster_bindings;

DROP POLICY project_memberships_scope ON project_memberships;
ALTER TABLE project_memberships NO FORCE ROW LEVEL SECURITY;
ALTER TABLE project_memberships DISABLE ROW LEVEL SECURITY;
DROP TRIGGER project_memberships_requires_tenant_member ON project_memberships;
DROP TABLE project_memberships;

DROP POLICY projects_scope ON projects;
ALTER TABLE projects NO FORCE ROW LEVEL SECURITY;
ALTER TABLE projects DISABLE ROW LEVEL SECURITY;
DROP TABLE projects;

DROP FUNCTION binding_requires_assigned_cluster();
DROP FUNCTION project_member_requires_tenant_member();
