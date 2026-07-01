package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/mubeendevelops/dispatch-go/internal/models"
)

// Sentinel errors for the user/account path. ErrEmailTaken is a 409 (the email is
// already registered); ErrUserNotFound is mapped to a generic 401 by the login
// handler so it never reveals whether an email exists.
var (
	ErrEmailTaken   = errors.New("email already registered")
	ErrUserNotFound = errors.New("user not found")
)

// CreateAccount provisions a brand-new tenant AND its first user in a single
// transaction, so signup is all-or-nothing -- we never end up with a tenant that
// has no user (or a user with no tenant). This mirrors the store's other
// multi-row invariants (DeadLetter, RetryJob) being transactions.
//
// MVP models one user per tenant, so the tenant is named after the user's email;
// the User/Tenant split still lets "teams" (many users, one tenant) arrive later
// without a schema change. email must already be normalized (trim + lowercase) by
// the caller; passwordHash is a bcrypt hash. A duplicate email trips the
// unique(lower(email)) index and returns ErrEmailTaken.
func (s *Store) CreateAccount(ctx context.Context, email, passwordHash string) (*models.Tenant, *models.User, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx) // no-op after a successful Commit

	var t models.Tenant
	if err := tx.QueryRow(ctx,
		`INSERT INTO tenants (name) VALUES ($1) RETURNING id, name, created_at`, email,
	).Scan(&t.ID, &t.Name, &t.CreatedAt); err != nil {
		return nil, nil, fmt.Errorf("create tenant: %w", err)
	}

	var u models.User
	err = tx.QueryRow(ctx,
		`INSERT INTO users (tenant_id, email, password_hash) VALUES ($1, $2, $3)
		 RETURNING id, tenant_id, email, password_hash, created_at`,
		t.ID, email, passwordHash,
	).Scan(&u.ID, &u.TenantID, &u.Email, &u.PasswordHash, &u.CreatedAt)
	if err != nil {
		// The unique(lower(email)) index is the authoritative guard against a
		// duplicate signup (races and all); translate its violation to ErrEmailTaken
		// rather than doing a separate, racy "does this email exist?" check first.
		if isUniqueViolation(err) {
			return nil, nil, ErrEmailTaken
		}
		return nil, nil, fmt.Errorf("create user: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	return &t, &u, nil
}

// GetUserByEmail loads a user by their email for the login path (case-insensitive,
// matching the unique(lower(email)) index). Missing -> ErrUserNotFound, which the
// handler maps to a generic 401 so it never discloses whether the email exists.
func (s *Store) GetUserByEmail(ctx context.Context, email string) (*models.User, error) {
	return s.scanUser(ctx,
		`SELECT id, tenant_id, email, password_hash, created_at FROM users WHERE lower(email) = lower($1)`, email)
}

// GetUserByID loads a user by id (used by GET /auth/me from the session's user).
func (s *Store) GetUserByID(ctx context.Context, id uuid.UUID) (*models.User, error) {
	return s.scanUser(ctx,
		`SELECT id, tenant_id, email, password_hash, created_at FROM users WHERE id = $1`, id)
}

// scanUser runs a single-row user query and maps a no-rows result to
// ErrUserNotFound.
func (s *Store) scanUser(ctx context.Context, q string, arg any) (*models.User, error) {
	var u models.User
	if err := s.pool.QueryRow(ctx, q, arg).Scan(
		&u.ID, &u.TenantID, &u.Email, &u.PasswordHash, &u.CreatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("get user: %w", err)
	}
	return &u, nil
}

// isUniqueViolation reports whether err is a Postgres unique-constraint violation
// (SQLSTATE 23505). Used to translate a duplicate insert into a domain sentinel
// (e.g. ErrEmailTaken) without a separate, race-prone existence check.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
