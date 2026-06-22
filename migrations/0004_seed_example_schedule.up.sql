-- 0004_seed_example_schedule.up.sql
--
-- Seed one example schedule so the scheduler has something to fire the moment you
-- run it: an "echo" job every minute. The id is a fixed sentinel UUID and the
-- INSERT is idempotent (ON CONFLICT DO NOTHING), so re-running migrations never
-- creates a duplicate. next_run_at defaults to now(), so the schedule is "due"
-- right away -- it fires on the scheduler's first tick, then advances to each
-- following minute.

INSERT INTO job_schedules (id, job_type, payload, cron_expression, enabled)
VALUES (
    '00000000-0000-0000-0000-000000000001',
    'echo',
    '{"source": "scheduler", "note": "fires every minute"}'::jsonb,
    '* * * * *',
    true
)
ON CONFLICT (id) DO NOTHING;
