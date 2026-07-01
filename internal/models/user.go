package models

import (
	"time"

	"github.com/google/uuid"
)

// User is a human who authenticates to the dashboard with email + password. It
// maps onto the `users` table.
//
// A user belongs to exactly one Tenant (the isolation/billing boundary). The MVP
// creates one user per tenant at signup, but User and Tenant are modelled
// separately so multi-user "teams" become a later additive change, not a schema
// rewrite. The bcrypt PasswordHash carries json:"-" so it is never serialized --
// the API must not leak even the hash.
type User struct {
	ID           uuid.UUID `json:"id"`
	TenantID     uuid.UUID `json:"tenant_id"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"` // bcrypt hash; never sent to a client
	CreatedAt    time.Time `json:"created_at"`
}
