-- 0006_create_users.up.sql
--
-- A user is a human who logs into the dashboard with email + password. It is
-- modelled separately from the tenant (the isolation/billing boundary) so that
-- "teams" -- many users, one tenant -- later become an additive change, not a
-- schema rewrite. The MVP creates exactly one user per tenant at signup, but the
-- schema does not bake that assumption in.
--
-- The password is stored ONLY as a bcrypt hash (slow, salted, adaptive) -- never
-- the plaintext. Email is the login identifier: a user is looked up by email
-- alone (before any tenant is known), so it must be GLOBALLY unique. We enforce
-- that case-insensitively via a UNIQUE index on lower(email); the application also
-- normalizes (trim + lowercase) before writing, so the two never disagree.
--
-- Part of the reserved 0006-0008 auth block (see 0011's header). users FK to the
-- tenants table created in 0005; ON DELETE CASCADE so purging a tenant removes its
-- users too.

CREATE TABLE IF NOT EXISTS users (
    id            UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID         NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    email         TEXT         NOT NULL,
    password_hash TEXT         NOT NULL,
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- Global, case-insensitive uniqueness of the login identifier. The functional
-- index on lower(email) is ALSO the lookup index the login path uses (it queries
-- WHERE lower(email) = lower($1)), so one index serves both roles.
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_email ON users (lower(email));

-- Index the FK: Postgres does NOT auto-index foreign keys, so without this a
-- tenant cascade-delete would scan the whole users table. Also serves a future
-- "users in this tenant" listing.
CREATE INDEX IF NOT EXISTS idx_users_tenant ON users (tenant_id);
