package models

import (
	"time"

	"github.com/google/uuid"
)

// APIKey is a tenant's programmatic credential for the public /api/v1 job API. It
// maps onto the `api_keys` table.
//
// Only a hash of the key is ever stored; the full secret is shown to the caller
// exactly once, at creation, and never persisted. KeyHash therefore carries
// json:"-" (never serialized), and there is no plaintext field on this stored
// model at all -- the one-time full key is returned via a separate creation
// response shape in the handler, not through this type.
//
// KeyPrefix is a short, non-secret display slice (e.g. dk_ab12cd34) so the
// dashboard can show which key is which without ever revealing the secret again.
type APIKey struct {
	ID         uuid.UUID  `json:"id"`
	TenantID   uuid.UUID  `json:"tenant_id"`
	Name       string     `json:"name"`
	KeyHash    string     `json:"-"` // SHA-256 of the full key; never sent to a client
	KeyPrefix  string     `json:"key_prefix"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}
