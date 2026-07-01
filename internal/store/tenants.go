package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/mubeendevelops/dispatch-go/internal/models"
)

// ErrTenantNotFound is returned when a tenant id resolves to no row.
var ErrTenantNotFound = errors.New("tenant not found")

// GetTenant loads a tenant by id. It backs GET /auth/me, which returns the
// caller's tenant alongside their user. (Tenant creation is not here: a tenant is
// only ever created together with its first user, atomically, in CreateAccount.)
func (s *Store) GetTenant(ctx context.Context, id uuid.UUID) (*models.Tenant, error) {
	const q = `SELECT id, name, created_at FROM tenants WHERE id = $1`
	var t models.Tenant
	if err := s.pool.QueryRow(ctx, q, id).Scan(&t.ID, &t.Name, &t.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrTenantNotFound
		}
		return nil, fmt.Errorf("get tenant: %w", err)
	}
	return &t, nil
}
