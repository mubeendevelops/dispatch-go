-- 0001_create_jobs_table.down.sql
-- Reverses the up migration. Dropping the table also drops its trigger.
DROP TABLE IF EXISTS jobs;
DROP FUNCTION IF EXISTS set_updated_at();
