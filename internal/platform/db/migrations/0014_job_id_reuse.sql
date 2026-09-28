-- +goose Up
DROP INDEX jobs_slurm;
CREATE UNIQUE INDEX jobs_slurm ON jobs(cluster_id, slurm_job_id)
  WHERE slurm_job_id IS NOT NULL
    AND state IN ('SUBMITTING', 'QUEUED', 'RUNNING');

ALTER TABLE jobs
  ADD COLUMN adopt_version_conflicts integer NOT NULL DEFAULT 0
    CHECK (adopt_version_conflicts >= 0);

-- +goose Down
DROP INDEX jobs_slurm;
CREATE UNIQUE INDEX jobs_slurm ON jobs(cluster_id, slurm_job_id)
  WHERE slurm_job_id IS NOT NULL;

ALTER TABLE jobs DROP COLUMN adopt_version_conflicts;
