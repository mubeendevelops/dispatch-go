-- 0004_seed_example_schedule.down.sql
-- Removes the seeded example schedule by its fixed sentinel id.
DELETE FROM job_schedules WHERE id = '00000000-0000-0000-0000-000000000001';
