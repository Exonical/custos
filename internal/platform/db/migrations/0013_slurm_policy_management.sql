-- +goose Up
ALTER TABLE clusters
  ADD COLUMN policy_management text NOT NULL DEFAULT 'inherit'
    CHECK (policy_management IN ('inherit','enforce','report')),
  ADD COLUMN policy_parent_account text NOT NULL DEFAULT 'root';

ALTER TABLE project_cluster_bindings
  DROP CONSTRAINT project_cluster_bindings_drift_state_check,
  ADD CONSTRAINT project_cluster_bindings_drift_state_check
    CHECK (drift_state IN ('ok','drift','unknown','error'));

ALTER TABLE policy_sync_status
  ADD COLUMN last_applied_at timestamptz,
  ADD COLUMN ops_applied integer NOT NULL DEFAULT 0,
  ADD COLUMN ops_failed integer NOT NULL DEFAULT 0;

CREATE TABLE slurm_managed_objects (
  cluster_id uuid NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
  kind text NOT NULL CHECK (kind IN ('account','association')),
  key text NOT NULL,
  binding_id uuid REFERENCES project_cluster_bindings(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY(cluster_id,kind,key));
ALTER TABLE slurm_managed_objects ENABLE ROW LEVEL SECURITY;
ALTER TABLE slurm_managed_objects FORCE ROW LEVEL SECURITY;
CREATE POLICY slurm_managed_objects_platform ON slurm_managed_objects
  USING (current_setting('app.tenant_id',true)='*')
  WITH CHECK (current_setting('app.tenant_id',true)='*');

-- +goose Down
DROP POLICY slurm_managed_objects_platform ON slurm_managed_objects;
DROP TABLE slurm_managed_objects;
ALTER TABLE policy_sync_status
  DROP COLUMN last_applied_at,
  DROP COLUMN ops_applied,
  DROP COLUMN ops_failed;
ALTER TABLE project_cluster_bindings
  DROP CONSTRAINT project_cluster_bindings_drift_state_check;
UPDATE project_cluster_bindings SET drift_state='unknown' WHERE drift_state='error';
ALTER TABLE project_cluster_bindings
  ADD CONSTRAINT project_cluster_bindings_drift_state_check
    CHECK (drift_state IN ('ok','drift','unknown'));
ALTER TABLE clusters DROP COLUMN policy_parent_account, DROP COLUMN policy_management;
