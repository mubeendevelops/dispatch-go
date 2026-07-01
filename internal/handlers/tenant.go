package handlers

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/mubeendevelops/dispatch-go/internal/models"
)

// This file is the request-scoped identity seam. Auth middleware resolves the
// caller to a tenant (and, for a browser session, a user) and stashes them in the
// request context; every handler reads the tenant back and passes it to the
// tenant-scoped store, so isolation is enforced structurally rather than by each
// handler remembering to filter.
//
// This seam is why swapping auth mechanisms didn't touch the handlers. Phase A put
// a single fixed default tenant here (a DefaultTenant shim); Phase B replaced that
// shim with real auth -- RequireAPIKey / RequireSession, in auth.go -- that derives
// the tenant from a Bearer API key or a session cookie. The handlers never changed:
// they still pull the tenant from context via the same helper.

// contextKey is an unexported type for context keys defined in this package, so
// values set here can't collide with keys set by other packages sharing the ctx.
type contextKey int

const (
	tenantContextKey contextKey = iota
	userContextKey
)

// WithTenant returns a copy of ctx carrying the tenant id. Auth middleware calls
// this once per request; handlers read it back with tenantFromContext.
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

// WithUser returns a copy of ctx carrying the authenticated user. RequireSession
// sets this (alongside the tenant) so dashboard handlers like GET /auth/me can
// identify the human. RequireAPIKey does NOT set it: a program authenticating with
// an API key has a tenant but no user.
func WithUser(ctx context.Context, user *models.User) context.Context {
	return context.WithValue(ctx, userContextKey, user)
}

// userFromContext extracts the user a session middleware injected. ok is false on
// an API-key request (no user) or if no session middleware ran.
func userFromContext(ctx context.Context) (*models.User, bool) {
	u, ok := ctx.Value(userContextKey).(*models.User)
	return u, ok
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

// user pulls the authenticated user from context for a session-scoped handler,
// writing a 500 and returning ok=false if absent (a wiring bug -- RequireSession
// must run first). Fail-closed, exactly like tenantID.
func (h *Handler) user(w http.ResponseWriter, r *http.Request) (*models.User, bool) {
	u, ok := userFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusInternalServerError, "no user in request context")
		return nil, false
	}
	return u, true
}
