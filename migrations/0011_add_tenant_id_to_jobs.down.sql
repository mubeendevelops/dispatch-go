-- 0011_add_tenant_id_to_jobs.down.sql
--
-- Reverses the up migration. Order matters only for readability here (dropping a
-- column drops its FK constraint and any index that references it), but we undo the
-- tables in the reverse order we built them and restore the two job indexes the up
-- migration replaced, so the schema returns exactly to its pre-0011 shape.

-- dead_letter_queue
DROP INDEX IF EXISTS idx_dlq_tenant;
ALTER TABLE dead_letter_queue DROP CONSTRAINT IF EXISTS dead_letter_queue_tenant_id_fkey;
ALTER TABLE dead_letter_queue DROP COLUMN IF EXISTS tenant_id;

-- job_schedules
DROP INDEX IF EXISTS idx_job_schedules_tenant;
ALTER TABLE job_schedules DROP CONSTRAINT IF EXISTS job_schedules_tenant_id_fkey;
ALTER TABLE job_schedules DROP COLUMN IF EXISTS tenant_id;

-- jobs: drop the tenant-first composites and restore the original single-column indexes.
DROP INDEX IF EXISTS idx_jobs_tenant_status;
DROP INDEX IF EXISTS idx_jobs_tenant_created_at;
CREATE INDEX IF NOT EXISTS idx_jobs_status     ON jobs (status);
CREATE INDEX IF NOT EXISTS idx_jobs_created_at ON jobs (created_at DESC);
ALTER TABLE jobs DROP CONSTRAINT IF EXISTS jobs_tenant_id_fkey;
ALTER TABLE jobs DROP COLUMN IF EXISTS tenant_id;
