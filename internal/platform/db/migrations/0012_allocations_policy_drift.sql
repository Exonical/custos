-- +goose Up
CREATE TABLE policy_sync_status (
  cluster_id uuid PRIMARY KEY REFERENCES clusters(id) ON DELETE CASCADE,
  checked_at timestamptz,
  last_error text
);
ALTER TABLE policy_sync_status ENABLE ROW LEVEL SECURITY;
ALTER TABLE policy_sync_status FORCE ROW LEVEL SECURITY;
CREATE POLICY policy_sync_status_platform ON policy_sync_status
  USING (current_setting('app.tenant_id',true)='*')
  WITH CHECK (current_setting('app.tenant_id',true)='*');

ALTER TABLE project_cluster_bindings
  ADD COLUMN drift jsonb NOT NULL DEFAULT '[]',
  ADD COLUMN drift_checked_at timestamptz,
  ADD COLUMN drift_state text NOT NULL DEFAULT 'unknown'
    CHECK (drift_state IN ('ok','drift','unknown'));

ALTER TABLE jobs
  ADD COLUMN binding_id uuid REFERENCES project_cluster_bindings(id) ON DELETE SET NULL,
  ADD COLUMN estimated_cost jsonb;
CREATE INDEX jobs_binding_active ON jobs(binding_id)
  WHERE state IN ('SUBMITTING','QUEUED','RUNNING');

CREATE TABLE allocations (
  id uuid PRIMARY KEY,
  tenant_id uuid NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  project_id uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  binding_id uuid NOT NULL REFERENCES project_cluster_bindings(id) ON DELETE CASCADE,
  name text NOT NULL,
  unit text NOT NULL CHECK (unit IN ('cpu_hours','gpu_hours','node_hours')),
  limit_amount numeric(18,3) NOT NULL CHECK (limit_amount >= 0),
  period_start timestamptz NOT NULL,
  period_end timestamptz NOT NULL CHECK (period_end > period_start),
  enforcement text NOT NULL CHECK (enforcement IN ('hard','soft')),
  consumed_amount numeric(18,3) NOT NULL DEFAULT 0,
  consumed_as_of timestamptz,
  version integer NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  created_by uuid NOT NULL REFERENCES users(id),
  UNIQUE(binding_id,name));
CREATE INDEX allocations_tenant_project ON allocations(tenant_id,project_id);

-- Binding/project/tenant consistency independent of the service layer.
-- +goose StatementBegin
CREATE FUNCTION allocation_binding_matches_scope() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM project_cluster_bindings b
    WHERE b.id=NEW.binding_id AND b.project_id=NEW.project_id AND b.tenant_id=NEW.tenant_id) THEN
    RAISE EXCEPTION 'allocation binding does not belong to project/tenant';
  END IF;
  RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER allocations_binding_scope BEFORE INSERT OR UPDATE ON allocations
  FOR EACH ROW EXECUTE FUNCTION allocation_binding_matches_scope();

ALTER TABLE allocations ENABLE ROW LEVEL SECURITY;
ALTER TABLE allocations FORCE ROW LEVEL SECURITY;
CREATE POLICY allocations_scope ON allocations
  USING (current_setting('app.tenant_id',true)='*' OR tenant_id::text=current_setting('app.tenant_id',true))
  WITH CHECK (current_setting('app.tenant_id',true)='*' OR tenant_id::text=current_setting('app.tenant_id',true));

-- +goose Down
DROP POLICY allocations_scope ON allocations;
DROP TRIGGER allocations_binding_scope ON allocations;
DROP FUNCTION allocation_binding_matches_scope();
DROP TABLE allocations;
ALTER TABLE jobs DROP COLUMN estimated_cost, DROP COLUMN binding_id;
ALTER TABLE project_cluster_bindings DROP COLUMN drift, DROP COLUMN drift_checked_at, DROP COLUMN drift_state;
DROP POLICY policy_sync_status_platform ON policy_sync_status;
DROP TABLE policy_sync_status;
