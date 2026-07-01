-- 0005_create_tenants.down.sql
-- Reverses the up migration. Run 0011's down first: while tenant_id foreign keys on
-- jobs / job_schedules / dead_letter_queue still reference tenants(id), this DROP
-- would fail.
DROP TABLE IF EXISTS tenants;
