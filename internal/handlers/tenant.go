package handlers

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/mubeendevelops/dispatch-go/internal/models"
)

// This file is the request-scoped tenancy seam. Every handler reads the caller's
// tenant from the request context and passes it to the (now tenant-scoped) store,
// so isolation is enforced structurally rather than by each handler remembering to
// filter.
//
// PHASE A vs PHASE B: today the DefaultTenant middleware injects a single fixed
// tenant, so the external API behaves exactly as before while every job is owned.
// Phase B replaces *only* that middleware with real auth (RequireAPIKey /
// RequireSession) that derives the tenant from the Bearer key or session cookie.
// The handlers below don't change across that swap -- they already pull the tenant
// from context via the same helper -- which is the whole point of routing it
// through the context instead of hard-coding a tenant in each handler.

// contextKey is an unexported type for context keys defined in this package, so
// values set here can't collide with keys set by other packages sharing the ctx.
type contextKey int

const tenantContextKey contextKey = iota

// WithTenant returns a copy of ctx carrying the tenant id. Auth middleware (or the
// Phase-A DefaultTenant shim) calls this once per request; handlers read it back
// with tenantFromContext.
func WithTenant(ctx context.Context, tenantID uuid.UUID) context.Context {
	return context.WithValue(ctx, tenantContextKey, tenantID)
}

// tenantFromContext extracts the tenant id a middleware injected. ok is false if no
// middleware ran -- a wiring bug -- which handlers treat as fail-closed (500)
// rather than silently falling back to some tenant.
func tenantFromContext(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(tenantContextKey).(uuid.UUID)
	return id, ok
}

// DefaultTenant is the Phase-A bridge middleware: it attributes every request to
// the seeded default tenant. It stands in for real authentication until Phase B,
// keeping the API usable (and every job owned) without yet having signup/login or
// API keys. Swapping this one middleware for RequireAPIKey/RequireSession is the
// only change Phase B needs at this seam.
func DefaultTenant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(WithTenant(r.Context(), models.DefaultTenantID)))
	})
}

// tenantID pulls the request's tenant from context for a handler, writing a 500 and
// returning ok=false if it is absent. Handlers guard on ok before touching the
// store, so a missing tenant fails the request instead of running unscoped.
func (h *Handler) tenantID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, ok := tenantFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "no tenant in request context")
		return uuid.Nil, false
	}
	return id, true
}
