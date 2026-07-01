-- 0005_create_tenants.up.sql
--
-- The tenant is the isolation AND billing boundary of the multi-tenant system:
-- everything a customer owns (jobs, schedules, dead-lettered jobs, and -- in later
-- phases -- users, API keys, and usage) hangs off a tenant_id. It is modelled
-- separately from a "user" on purpose, so that "teams" (many users, one tenant)
-- later become an additive change, not a schema rewrite -- even though the MVP
-- creates exactly one user per tenant at signup.
--
-- This migration only introduces the tenant itself. Auth (users/sessions/api_keys,
-- migrations 0006-0008) and billing columns (Stripe ids, plan, status -- added in
-- the metering phase) are deliberately left out so each phase adds just what it
-- needs. gen_random_uuid() is built into Postgres 13+ (no pgcrypto extension).

CREATE TABLE IF NOT EXISTS tenants (
    id          UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT         NOT NULL,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- Seed the DEFAULT tenant with a fixed sentinel id. It exists for two reasons:
--
--   1. Backfill: migration 0011 adds tenant_id to the already-populated jobs,
--      job_schedules, and dead_letter_queue tables and points every pre-existing
--      row at this tenant, so the NOT NULL + FK can be added without data loss.
--   2. Pre-auth bridge: until Phase B wires real per-request tenancy (API keys /
--      sessions), the API attributes newly enqueued jobs to this tenant, so the
--      external API keeps working unchanged while every job is nonetheless owned.
--
-- The id must be NON-NIL: the enqueue path rejects a uuid.Nil ("unowned") tenant as
-- a bug, so the default owner must be a real, distinct value. The all-ones sentinel
-- is chosen to be visually distinct from the 0004 seeded schedule's ...0001 id.
-- ON CONFLICT DO NOTHING keeps re-running migrations idempotent.
INSERT INTO tenants (id, name)
VALUES ('11111111-1111-1111-1111-111111111111', 'default')
ON CONFLICT (id) DO NOTHING;
