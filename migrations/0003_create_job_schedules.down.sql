-- 0003_create_job_schedules.down.sql
-- Reverses the up migration (dropping the table drops its index too).
DROP TABLE IF EXISTS job_schedules;
