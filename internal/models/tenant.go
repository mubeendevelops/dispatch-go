package models

import (
	"time"

	"github.com/google/uuid"
)

// Tenant is the isolation and billing boundary: everything a customer owns (jobs,
// schedules, dead-lettered jobs, and -- in later phases -- users, API keys, and
// usage) hangs off a tenant's id. It maps onto the `tenants` table.
//
// It is modelled separately from a "user" on purpose so that multi-user tenants
// ("teams": many users, one tenant) become a later additive change rather than a
// schema rewrite, even though the MVP creates exactly one user per tenant at
// signup. Billing fields (Stripe ids, plan, subscription status) are added in the
// metering phase, not here.
type Tenant struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}
