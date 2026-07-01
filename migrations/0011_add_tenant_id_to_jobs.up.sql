-- 0011_add_tenant_id_to_jobs.up.sql
--
-- Retrofit tenancy onto the three pre-existing owned-data tables: jobs,
-- job_schedules, and dead_letter_queue. This is where multi-tenant isolation
-- becomes a *data* fact -- every owned row now carries the tenant it belongs to,
-- and the store layer scopes every query by it.
--
-- Each table follows the standard safe pattern for adding a required column to a
-- populated table:
--     add the column NULLABLE  ->  backfill every existing row  ->  SET NOT NULL
-- Doing it in that order means the ALTER never rejects existing rows for being
-- null. Existing rows predate tenancy, so they are assigned to the seeded default
-- tenant (migration 0005). The FK (ON DELETE CASCADE) then guarantees a job/
-- schedule/DLQ row can only ever name a tenant that exists, and that purging a
-- tenant cleanly removes everything it owns.
--
-- The numbering gap (0006-0010) is intentional: those slots are reserved for the
-- auth tables (users/sessions/api_keys) and billing tables (usage_records/
-- stripe_events) that later phases add. The migration runner tracks each version
-- independently, so applying 0005 then 0011 now -- and 0006-0010 later -- is safe.

-- The default tenant every pre-tenancy row is backfilled to (seeded in 0005).

-- ---------------------------------------------------------------------------
-- jobs
-- ---------------------------------------------------------------------------
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS tenant_id UUID;
UPDATE jobs SET tenant_id = '11111111-1111-1111-1111-111111111111' WHERE tenant_id IS NULL;
ALTER TABLE jobs ALTER COLUMN tenant_id SET NOT NULL;
ALTER TABLE jobs ADD CONSTRAINT jobs_tenant_id_fkey
    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE;

-- Re-index for tenancy. Every dashboard query now leads with `tenant_id = ?`, so
-- tenant_id must be the LEFTMOST column of the index or Postgres can't use it as
-- the access predicate (it would filter tenant_id only after scanning by status /
-- created_at). Replace the two single-column indexes that tenancy supersedes with
-- tenant-first composites the per-tenant queries actually use.
DROP INDEX IF EXISTS idx_jobs_status;      -- superseded by (tenant_id, status)
DROP INDEX IF EXISTS idx_jobs_created_at;   -- superseded by (tenant_id, created_at DESC)
CREATE INDEX IF NOT EXISTS idx_jobs_tenant_status      ON jobs (tenant_id, status);
CREATE INDEX IF NOT EXISTS idx_jobs_tenant_created_at  ON jobs (tenant_id, created_at DESC);
-- idx_jobs_queue_name / idx_jobs_queue_status are left as-is: they serve queue-
-- scoped operator lookups (which are cross-tenant), not the per-tenant dashboard.

-- ---------------------------------------------------------------------------
-- job_schedules
-- ---------------------------------------------------------------------------
-- Schedules become tenant-owned; a fired job inherits its schedule's tenant (the
-- scheduler copies it onto each enqueued job). The scheduler's due-scan itself
-- stays global -- it is trusted infrastructure firing every tenant's schedules.
ALTER TABLE job_schedules ADD COLUMN IF NOT EXISTS tenant_id UUID;
UPDATE job_schedules SET tenant_id = '11111111-1111-1111-1111-111111111111' WHERE tenant_id IS NULL;
ALTER TABLE job_schedules ALTER COLUMN tenant_id SET NOT NULL;
ALTER TABLE job_schedules ADD CONSTRAINT job_schedules_tenant_id_fkey
    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE;

-- Index the FK column: Postgres does NOT auto-index foreign keys, and an unindexed
-- FK makes a parent DELETE (tenant cascade) scan the whole child table. Also serves
-- a future "my schedules" per-tenant listing.
CREATE INDEX IF NOT EXISTS idx_job_schedules_tenant ON job_schedules (tenant_id);

-- ---------------------------------------------------------------------------
-- dead_letter_queue
-- ---------------------------------------------------------------------------
-- A DLQ row is derived from a job, so its tenant is backfilled FROM that job rather
-- than blindly to the default tenant -- a DLQ row must never name a different
-- tenant than the job it describes. (The application preserves this same invariant
-- going forward by deriving tenant_id from the jobs row when it inserts the DLQ
-- record inside the dead-letter transaction.)
ALTER TABLE dead_letter_queue ADD COLUMN IF NOT EXISTS tenant_id UUID;
UPDATE dead_letter_queue d SET tenant_id = j.tenant_id
    FROM jobs j WHERE d.job_id = j.id AND d.tenant_id IS NULL;
ALTER TABLE dead_letter_queue ALTER COLUMN tenant_id SET NOT NULL;
ALTER TABLE dead_letter_queue ADD CONSTRAINT dead_letter_queue_tenant_id_fkey
    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE;

CREATE INDEX IF NOT EXISTS idx_dlq_tenant ON dead_letter_queue (tenant_id);
