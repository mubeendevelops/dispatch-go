package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/mubeendevelops/dispatch-go/internal/models"
)

// ErrAPIKeyNotFound is returned when a key id doesn't exist for the tenant (a 404
// on delete). API-key AUTH failures don't use this -- the middleware returns a
// generic 401 and never reveals whether a key existed.
var ErrAPIKeyNotFound = errors.New("api key not found")

// CreateAPIKey stores a new key for a tenant. keyHash is the SHA-256 of the full
// key (never the plaintext); keyPrefix is the non-secret display slice. The
// returned APIKey carries no plaintext -- the caller shows the full key (which
// only it holds) to the user once, then it's unrecoverable.
func (s *Store) CreateAPIKey(ctx context.Context, tenantID uuid.UUID, name, keyHash, keyPrefix string) (*models.APIKey, error) {
	const q = `
		INSERT INTO api_keys (tenant_id, name, key_hash, key_prefix)
		VALUES ($1, $2, $3, $4)
		RETURNING id, tenant_id, name, key_hash, key_prefix, last_used_at, created_at`
	var k models.APIKey
	if err := s.pool.QueryRow(ctx, q, tenantID, name, keyHash, keyPrefix).Scan(
		&k.ID, &k.TenantID, &k.Name, &k.KeyHash, &k.KeyPrefix, &k.LastUsedAt, &k.CreatedAt,
	); err != nil {
		return nil, fmt.Errorf("create api key: %w", err)
	}
	return &k, nil
}

// ListAPIKeys returns a tenant's keys, newest first. It never selects key_hash --
// the dashboard only needs the metadata (name, prefix, timestamps). Scoping by
// tenant_id is what stops one tenant from listing another's keys.
func (s *Store) ListAPIKeys(ctx context.Context, tenantID uuid.UUID) ([]models.APIKey, error) {
	const q = `
		SELECT id, tenant_id, name, key_prefix, last_used_at, created_at
		FROM api_keys WHERE tenant_id = $1 ORDER BY created_at DESC`
	rows, err := s.pool.Query(ctx, q, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	defer rows.Close()

	keys := make([]models.APIKey, 0) // non-nil so an empty list marshals to []
	for rows.Next() {
		var k models.APIKey
		if err := rows.Scan(&k.ID, &k.TenantID, &k.Name, &k.KeyPrefix, &k.LastUsedAt, &k.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan api key: %w", err)
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// DeleteAPIKey revokes a key, scoped to the owning tenant so one tenant can never
// delete another's key (a mismatched tenant reads as "not found", never revealing
// the key exists). Returns ErrAPIKeyNotFound when nothing was deleted.
func (s *Store) DeleteAPIKey(ctx context.Context, tenantID, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM api_keys WHERE id = $1 AND tenant_id = $2`, id, tenantID)
	if err != nil {
		return fmt.Errorf("delete api key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrAPIKeyNotFound
	}
	return nil
}

// AuthenticateAPIKey resolves a key hash to its owning tenant for RequireAPIKey. It
// is a pure read -- no last_used_at write on the hot path; that's the best-effort
// TouchAPIKey below. A miss returns ErrAPIKeyNotFound, which the middleware turns
// into a generic 401. It also returns the key id so the middleware can touch it.
func (s *Store) AuthenticateAPIKey(ctx context.Context, keyHash string) (tenantID, keyID uuid.UUID, err error) {
	const q = `SELECT tenant_id, id FROM api_keys WHERE key_hash = $1`
	if err := s.pool.QueryRow(ctx, q, keyHash).Scan(&tenantID, &keyID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, uuid.Nil, ErrAPIKeyNotFound
		}
		return uuid.Nil, uuid.Nil, fmt.Errorf("authenticate api key: %w", err)
	}
	return tenantID, keyID, nil
}

// TouchAPIKey stamps last_used_at = now() for a key. It is BEST-EFFORT: the
// middleware calls it after a successful auth and ignores any error, so a failed
// timestamp update never turns a valid key into a 401. It is a separate statement
// from AuthenticateAPIKey so authentication stays a pure read (a GET shouldn't have
// to write to succeed); the cost is one cheap extra UPDATE per authenticated call.
func (s *Store) TouchAPIKey(ctx context.Context, keyID uuid.UUID) error {
	if _, err := s.pool.Exec(ctx, `UPDATE api_keys SET last_used_at = now() WHERE id = $1`, keyID); err != nil {
		return fmt.Errorf("touch api key: %w", err)
	}
	return nil
}
