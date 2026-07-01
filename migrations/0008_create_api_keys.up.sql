-- 0008_create_api_keys.up.sql
--
-- An API key is the programmatic credential for the public /api/v1 job API: a
-- tenant's own programs authenticate with `Authorization: Bearer dk_...`. Keys
-- belong to the TENANT (not a user), because they authorize machine access to the
-- tenant's jobs and must outlive any individual user.
--
-- Storage mirrors sessions -- store a HASH, never the plaintext -- with one
-- deliberate difference: API keys are hashed with plain SHA-256, NOT peppered with
-- SESSION_SECRET. Rationale: keys are long-lived, so their validity must not
-- depend on a rotatable server secret (rotating the pepper would silently break
-- every tenant's integration). A 256-bit crypto/rand key makes plain SHA-256 safe,
-- and being deterministic it gives an O(1) indexed lookup on every request --
-- which is also why bcrypt is the WRONG tool here: each bcrypt hash carries its own
-- salt, so you could not look a key up by hash; you'd have to scan and bcrypt-
-- compare every row.
--
-- key_prefix is a short, non-secret display slice (e.g. dk_ab12cd34) shown in the
-- dashboard so a human can recognize a key without ever seeing the full secret
-- again -- the full key is returned exactly once, at creation. last_used_at is a
-- best-effort "when was this key last seen" stamp for the dashboard.

CREATE TABLE IF NOT EXISTS api_keys (
    id            UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID         NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name          TEXT         NOT NULL,
    key_hash      TEXT         NOT NULL,
    key_prefix    TEXT         NOT NULL,
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    last_used_at  TIMESTAMPTZ
);

-- Per-request auth looks a key up by key_hash; UNIQUE enforces no two keys share a
-- hash and provides that lookup index.
CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_key_hash ON api_keys (key_hash);

-- Index the FK (Postgres doesn't auto-index FKs) so the tenant-scoped "list my
-- keys" query and a tenant cascade-delete don't scan the whole table.
CREATE INDEX IF NOT EXISTS idx_api_keys_tenant ON api_keys (tenant_id);
