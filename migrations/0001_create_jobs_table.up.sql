-- 0001_create_jobs_table.up.sql
--
-- The jobs table is the single source of truth for every job in the system.
-- Redis only ever holds a job's ID as a queue signal; all durable state lives here.
--
-- We persist a job here BEFORE pushing its ID to Redis (see CLAUDE.md). A crash
-- between the two steps can then only leave a recoverable row that was never
-- enqueued -- never the reverse (a queued ID that points at no row).
--
-- gen_random_uuid() is built into Postgres 13+ (no pgcrypto extension needed).
-- The application still generates the UUID itself (google/uuid) so it can return
-- the id to the caller and enqueue it without a round-trip; the DEFAULT is a
-- safety net for rows inserted by hand.

CREATE TABLE IF NOT EXISTS jobs (
    id              UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    queue_name      TEXT         NOT NULL DEFAULT 'default',
    job_type        TEXT         NOT NULL,
    payload         JSONB        NOT NULL DEFAULT '{}'::jsonb,
    status          TEXT         NOT NULL DEFAULT 'pending',
    result          JSONB,
    error_message   TEXT,
    retry_count     INTEGER      NOT NULL DEFAULT 0,
    max_retries     INTEGER      NOT NULL DEFAULT 0,
    scheduled_at    TIMESTAMPTZ,
    started_at      TIMESTAMPTZ,
    completed_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- Workers and the dashboard constantly filter by status ("show me failed jobs")
-- and by queue. These indexes keep those lookups off a full table scan.
CREATE INDEX IF NOT EXISTS idx_jobs_status      ON jobs (status);
CREATE INDEX IF NOT EXISTS idx_jobs_queue_name  ON jobs (queue_name);

-- The dashboard lists most-recent jobs first; a DESC index serves that ordering.
CREATE INDEX IF NOT EXISTS idx_jobs_created_at  ON jobs (created_at DESC);

-- Composite for the common "jobs in this queue with this status" stats query.
CREATE INDEX IF NOT EXISTS idx_jobs_queue_status ON jobs (queue_name, status);

-- Partial index for the (future) scheduler: it only cares about rows that carry
-- a scheduled_at, so we index just those and skip the huge majority of rows.
CREATE INDEX IF NOT EXISTS idx_jobs_scheduled_at ON jobs (scheduled_at)
    WHERE scheduled_at IS NOT NULL;

-- Keep updated_at honest at the database level so no code path can forget to bump
-- it on UPDATE. This is a DB-enforced invariant rather than a convention.
CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE TRIGGER trg_jobs_set_updated_at
    BEFORE UPDATE ON jobs
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();
