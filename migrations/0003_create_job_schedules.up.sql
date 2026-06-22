-- 0003_create_job_schedules.up.sql
--
-- job_schedules holds recurring job definitions. Each row is a cron expression
-- plus a template (job_type + payload); the scheduler (cmd/scheduler) turns each
-- due schedule into a real job on the work queue, once per fire.
--
-- next_run_at is the heart of the design: it is the durable "fire at or after
-- this time" cursor. Keeping it in Postgres -- not in the scheduler's memory --
-- is what makes the scheduler safe to restart: a fresh process reads next_run_at
-- and resumes exactly where the previous one stopped, with no double-fires. See
-- the "Recurring schedules" section in README.md for the full reasoning.

CREATE TABLE IF NOT EXISTS job_schedules (
    id              UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    job_type        TEXT         NOT NULL,
    payload         JSONB        NOT NULL DEFAULT '{}'::jsonb,
    cron_expression TEXT         NOT NULL,
    enabled         BOOLEAN      NOT NULL DEFAULT true,
    last_run_at     TIMESTAMPTZ,                         -- NULL until the first fire
    next_run_at     TIMESTAMPTZ  NOT NULL DEFAULT now(), -- a new row is due now, then snaps to the cron grid
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- The scheduler's hot query every tick is "enabled rows whose next_run_at has
-- passed". A partial index over just the enabled rows, ordered by next_run_at,
-- serves that lookup without touching disabled schedules.
CREATE INDEX IF NOT EXISTS idx_job_schedules_due
    ON job_schedules (next_run_at)
    WHERE enabled;
