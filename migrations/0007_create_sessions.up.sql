-- 0007_create_sessions.up.sql
--
-- A session is server-side auth state for a logged-in dashboard user. We use
-- database-backed sessions rather than a self-contained JWT for one reason:
-- REVOCABILITY. Logout (and a future "sign out everywhere") is just deleting a
-- row; a stateless JWT cannot be revoked before it expires. The cost is one
-- indexed lookup per request, which is negligible at this scale.
--
-- We store only a HASH of the opaque session token, never the token itself. The
-- token is a 256-bit crypto/rand value that lives in the user's cookie; the server
-- keeps HMAC-SHA256(SESSION_SECRET, token). So a read-only leak of this table does
-- NOT hand an attacker a working session -- they would still need the token
-- (infeasible to guess) and the secret (held separately, outside the DB). A fast
-- keyed hash is appropriate precisely because the token is already high-entropy:
-- unlike a password, it needs no slow KDF.
--
-- expires_at bounds a session's lifetime; the lookup filters out past ones and
-- expired rows can be reaped. Deleting the user cascades their sessions.

CREATE TABLE IF NOT EXISTS sessions (
    id          UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID         NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash  TEXT         NOT NULL,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    expires_at  TIMESTAMPTZ  NOT NULL
);

-- The per-request auth lookup is by token_hash, and a given hash identifies at
-- most one session -- UNIQUE both enforces that and provides the lookup index.
CREATE UNIQUE INDEX IF NOT EXISTS idx_sessions_token_hash ON sessions (token_hash);

-- Index the FK (Postgres doesn't auto-index FKs) so a user cascade-delete and a
-- future "sign out everywhere" (delete by user_id) don't scan the whole table.
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions (user_id);
