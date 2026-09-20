-- +goose Up
-- Immutable ended-job accounting facts plus recomputable daily aggregates.

ALTER TABLE jobs ADD COLUMN resource_usage jsonb;

CREATE TABLE usage_records (
  id uuid NOT NULL,
  cluster_id uuid NOT NULL REFERENCES clusters(id) ON DELETE RESTRICT,
  slurm_job_id bigint NOT NULL,
  slurm_job_name text NOT NULL,
  job_id uuid,
  tenant_id uuid,
  project_id uuid,
  user_id uuid,
  slurm_user text NOT NULL,
  account text NOT NULL,
  partition text NOT NULL,
  state text NOT NULL,
  exit_code integer,
  submit_time timestamptz,
  eligible_time timestamptz,
  start_time timestamptz,
  end_time timestamptz NOT NULL,
  elapsed_seconds bigint NOT NULL CHECK (elapsed_seconds >= 0),
  node_count integer NOT NULL CHECK (node_count >= 0),
  cpu_seconds bigint NOT NULL CHECK (cpu_seconds >= 0),
  gpu_seconds bigint NOT NULL CHECK (gpu_seconds >= 0),
  node_seconds bigint NOT NULL CHECK (node_seconds >= 0),
  mem_gb_seconds bigint NOT NULL CHECK (mem_gb_seconds >= 0),
  wait_seconds bigint,
  energy_joules bigint,
  tres_alloc jsonb NOT NULL DEFAULT '{}',
  tres_usage jsonb NOT NULL DEFAULT '{}',
  collected_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (id, end_time),
  UNIQUE (cluster_id, slurm_job_id, end_time)
) PARTITION BY RANGE (end_time);
CREATE INDEX usage_records_tenant_time ON usage_records (tenant_id, end_time DESC);
CREATE INDEX usage_records_job ON usage_records (job_id) WHERE job_id IS NOT NULL;

-- +goose StatementBegin
DO $$
DECLARE m date;
BEGIN
  FOR m IN SELECT generate_series(date '2026-01-01', date '2027-12-01', interval '1 month')::date LOOP
    EXECUTE format('CREATE TABLE usage_records_%s PARTITION OF usage_records FOR VALUES FROM (%L) TO (%L)',
      to_char(m, 'YYYYMM'), m, (m + interval '1 month')::date);
  END LOOP;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION usage_records_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'usage_records is append-only'; END;
$$;
-- +goose StatementEnd
CREATE TRIGGER usage_records_immutable_row BEFORE UPDATE OR DELETE ON usage_records
  FOR EACH ROW EXECUTE FUNCTION usage_records_immutable();
CREATE TRIGGER usage_records_immutable_truncate BEFORE TRUNCATE ON usage_records
  FOR EACH STATEMENT EXECUTE FUNCTION usage_records_immutable();

CREATE TABLE usage_daily (
  id uuid PRIMARY KEY,
  tenant_id uuid,
  project_id uuid,
  user_id uuid,
  cluster_id uuid NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
  account text NOT NULL DEFAULT '',
  partition text NOT NULL DEFAULT '',
  day date NOT NULL,
  scope_key text GENERATED ALWAYS AS
    (coalesce(tenant_id::text,'') || ':' || coalesce(project_id::text,'') || ':' || coalesce(user_id::text,'')) STORED,
  jobs bigint NOT NULL,
  failed bigint NOT NULL,
  cpu_seconds bigint NOT NULL,
  gpu_seconds bigint NOT NULL,
  node_seconds bigint NOT NULL,
  mem_gb_seconds bigint NOT NULL,
  wait_seconds_sum bigint NOT NULL,
  run_seconds_sum bigint NOT NULL,
  energy_joules bigint,
  wait_p50 double precision,
  wait_p90 double precision,
  wait_p99 double precision,
  run_p50 double precision,
  run_p90 double precision,
  run_p99 double precision,
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (cluster_id, day, scope_key, account, partition)
);
CREATE INDEX usage_daily_tenant_day ON usage_daily (tenant_id, day, id);

CREATE TABLE accounting_watermarks (
  cluster_id uuid PRIMARY KEY REFERENCES clusters(id) ON DELETE CASCADE,
  watermark timestamptz NOT NULL DEFAULT '1970-01-01',
  last_collected_at timestamptz,
  last_error text
);
CREATE TABLE usage_dirty_days (
  cluster_id uuid NOT NULL REFERENCES clusters(id) ON DELETE CASCADE,
  day date NOT NULL,
  PRIMARY KEY (cluster_id, day)
);

ALTER TABLE usage_records ENABLE ROW LEVEL SECURITY;
ALTER TABLE usage_records FORCE ROW LEVEL SECURITY;
CREATE POLICY usage_records_scope ON usage_records
  USING (current_setting('app.tenant_id', true) = '*'
         OR tenant_id::text = current_setting('app.tenant_id', true))
  WITH CHECK (current_setting('app.tenant_id', true) = '*');
ALTER TABLE usage_daily ENABLE ROW LEVEL SECURITY;
ALTER TABLE usage_daily FORCE ROW LEVEL SECURITY;
CREATE POLICY usage_daily_scope ON usage_daily
  USING (current_setting('app.tenant_id', true) = '*'
         OR tenant_id::text = current_setting('app.tenant_id', true))
  WITH CHECK (current_setting('app.tenant_id', true) = '*');
ALTER TABLE accounting_watermarks ENABLE ROW LEVEL SECURITY;
ALTER TABLE accounting_watermarks FORCE ROW LEVEL SECURITY;
CREATE POLICY accounting_watermarks_platform ON accounting_watermarks
  USING (current_setting('app.tenant_id', true) = '*')
  WITH CHECK (current_setting('app.tenant_id', true) = '*');
ALTER TABLE usage_dirty_days ENABLE ROW LEVEL SECURITY;
ALTER TABLE usage_dirty_days FORCE ROW LEVEL SECURITY;
CREATE POLICY usage_dirty_days_platform ON usage_dirty_days
  USING (current_setting('app.tenant_id', true) = '*')
  WITH CHECK (current_setting('app.tenant_id', true) = '*');

-- +goose Down
DROP POLICY usage_dirty_days_platform ON usage_dirty_days;
DROP TABLE usage_dirty_days;
DROP POLICY accounting_watermarks_platform ON accounting_watermarks;
DROP TABLE accounting_watermarks;
DROP POLICY usage_daily_scope ON usage_daily;
DROP TABLE usage_daily;
DROP POLICY usage_records_scope ON usage_records;
DROP TRIGGER usage_records_immutable_truncate ON usage_records;
DROP TRIGGER usage_records_immutable_row ON usage_records;
DROP TABLE usage_records;
DROP FUNCTION usage_records_immutable();
ALTER TABLE jobs DROP COLUMN resource_usage;
