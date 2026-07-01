package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/mubeendevelops/dispatch-go/internal/models"
)

// ErrSessionInvalid means no live session matched the presented token -- it never
// existed, was deleted by logout, or has expired. The middleware maps it to 401. A
// single sentinel for all three cases is deliberate: we don't tell the caller
// which, so a stale cookie and a forged one are indistinguishable.
var ErrSessionInvalid = errors.New("session invalid or expired")

// CreateSession records a new server-side session for userID. tokenHash is the
// keyed hash of the opaque cookie token (the plaintext is never stored); expiresAt
// bounds its lifetime.
func (s *Store) CreateSession(ctx context.Context, userID uuid.UUID, tokenHash string, expiresAt time.Time) error {
	const q = `INSERT INTO sessions (user_id, token_hash, expires_at) VALUES ($1, $2, $3)`
	if _, err := s.pool.Exec(ctx, q, userID, tokenHash, expiresAt); err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

// SessionUser resolves a session token hash to its owning user, enforcing expiry
// in the same query (expires_at > now()). It returns ErrSessionInvalid for a
// missing OR expired session so a caller can't distinguish the two. This is the
// per-request lookup RequireSession runs; the returned user carries the tenant_id
// the rest of the request is scoped to.
func (s *Store) SessionUser(ctx context.Context, tokenHash string) (*models.User, error) {
	const q = `
		SELECT u.id, u.tenant_id, u.email, u.password_hash, u.created_at
		FROM sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.expires_at > now()`
	var u models.User
	if err := s.pool.QueryRow(ctx, q, tokenHash).Scan(
		&u.ID, &u.TenantID, &u.Email, &u.PasswordHash, &u.CreatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSessionInvalid
		}
		return nil, fmt.Errorf("session user: %w", err)
	}
	return &u, nil
}

// DeleteSession removes a session by its token hash -- the logout path. Deleting a
// row is the whole reason we chose DB-backed sessions over a stateless JWT:
// revocation is immediate. Deleting a non-existent session is not an error, so
// logout is idempotent.
func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, tokenHash); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}
