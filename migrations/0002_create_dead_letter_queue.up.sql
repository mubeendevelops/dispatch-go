-- 0002_create_dead_letter_queue.up.sql
--
-- The dead-letter queue (DLQ) records jobs that exhausted their retries and were
-- given up on. The job row itself is marked status='failed'; this table is the
-- durable audit trail of *why* and *how many attempts* it took.
--
-- job_id is the PRIMARY KEY: a job is dead-lettered at most once (it is terminal),
-- so the job id is its natural key. The FK to jobs keeps the DLQ from referencing
-- a job that doesn't exist; ON DELETE CASCADE drops the DLQ row if the job is purged.

CREATE TABLE IF NOT EXISTS dead_letter_queue (
    job_id      UUID         PRIMARY KEY REFERENCES jobs(id) ON DELETE CASCADE,
    reason      TEXT         NOT NULL,
    attempts    INTEGER      NOT NULL,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- The dashboard lists most-recently dead-lettered jobs first.
CREATE INDEX IF NOT EXISTS idx_dlq_created_at ON dead_letter_queue (created_at DESC);
