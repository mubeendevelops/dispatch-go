// Package handlers contains the HTTP API: request decoding, validation, and
// consistent JSON responses. Durable state lives in store (Postgres); the work
// signal lives in queue (Redis).
//
// The handlers are split across files by concern: this file holds wiring (struct,
// constructor, routes, health); jobs.go has the /jobs endpoints; admin.go has the
// dashboard/stats endpoints; auth.go has signup/login/logout/me plus the two auth
// middlewares; keys.go has API-key CRUD; tenant.go is the request-scoped identity
// seam; middleware.go and params.go hold CORS, JSON 404/405, and request-parameter
// validation.
package handlers

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mubeendevelops/dispatch-go/internal/enqueue"
	"github.com/mubeendevelops/dispatch-go/internal/queue"
	"github.com/mubeendevelops/dispatch-go/internal/store"
)

// Session defaults applied when SessionConfig leaves them zero, so cmd/api only
// has to supply the security-relevant bits (secret, secure flag).
const (
	// DefaultSessionTTL is how long a session stays valid. A week balances not
	// nagging users to re-login against bounding how long a stolen cookie is useful.
	DefaultSessionTTL = 7 * 24 * time.Hour
	defaultCookieName = "dispatch_session"
)

// SessionConfig holds what the auth layer needs to issue and validate sessions.
// Secret is the pepper for hashing session tokens (see internal/auth); Secure gates
// the cookie's Secure flag (off for local http, on behind HTTPS); TTL and
// CookieName default via the consts above when left zero.
type SessionConfig struct {
	Secret     []byte
	Secure     bool
	TTL        time.Duration
	CookieName string
}

// Handler holds the dependencies the routes need.
type Handler struct {
	store    *store.Store
	queue    *queue.Queue
	enqueuer *enqueue.Enqueuer
	queues   []string // configured queues, surfaced (unioned with DB queues) in stats
	sessions SessionConfig
}

// New wires up a Handler. queues is the configured queue list (config.Queues), used
// by the admin stats endpoint so known queues always appear even at zero depth. The
// enqueuer is the shared persist-before-enqueue path cmd/scheduler also uses.
// sessions carries the auth/session settings; its TTL and cookie name default here
// when unset.
func New(s *store.Store, q *queue.Queue, queues []string, sessions SessionConfig) *Handler {
	if sessions.TTL == 0 {
		sessions.TTL = DefaultSessionTTL
	}
	if sessions.CookieName == "" {
		sessions.CookieName = defaultCookieName
	}
	return &Handler{store: s, queue: q, enqueuer: enqueue.New(s, q), queues: queues, sessions: sessions}
}

// Routes returns the API router. It is mounted at "/" by cmd/api.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()

	// Render routing misses as our standard JSON error shape, not chi's plain text.
	r.NotFound(h.notFound)
	r.MethodNotAllowed(h.methodNotAllowed)

	r.Get("/healthz", h.health)

	// Auth. signup/login are public -- they establish a session; logout/me require
	// one. These set/clear the httpOnly session cookie the dashboard authenticates
	// with.
	r.Route("/auth", func(r chi.Router) {
		r.Post("/signup", h.signup)
		r.Post("/login", h.login)

		r.Group(func(r chi.Router) {
			r.Use(h.RequireSession)
			r.Post("/logout", h.logout)
			r.Get("/me", h.me)
		})
	})

	r.Route("/api/v1", func(r chi.Router) {
		// Public programmatic job API: a tenant's own programs authenticate with an
		// API key (Authorization: Bearer dk_...). RequireAPIKey resolves the key to a
		// tenant and injects it; the job handlers are unchanged from Phase A -- they
		// read the tenant from context.
		//
		// PHASE C SEAM: the browser dashboard will also need to read these job
		// endpoints, authenticated by its session cookie rather than an API key. That
		// is a one-line change then -- wrap this group in a "session-or-key"
		// middleware -- deliberately left for Phase C, exactly as Phase A left the
		// auth seam for Phase B.
		r.Group(func(r chi.Router) {
			r.Use(h.RequireAPIKey)
			r.Get("/jobs", h.listJobs)
			r.Post("/jobs/enqueue", h.enqueueJob)
			r.Get("/jobs/{id}", h.getJob)
			r.Post("/jobs/{id}/retry", h.retryJob)
			r.Post("/jobs/{id}/cancel", h.cancelJob)
		})

		// Dashboard-only endpoints: API-key management and per-tenant stats -- for
		// humans in the browser, so they authenticate with the session cookie, not an
		// API key.
		r.Group(func(r chi.Router) {
			r.Use(h.RequireSession)
			r.Get("/keys", h.listKeys)
			r.Post("/keys", h.createKey)
			r.Delete("/keys/{id}", h.deleteKey)

			r.Get("/admin/stats", h.stats)
			r.Get("/admin/dashboard", h.dashboard)
		})
	})

	return r
}

// health reports liveness of the API and its dependencies. Returns 503 if either
// Postgres or Redis is unreachable so a load balancer can route around it.
func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	resp := map[string]string{"status": "ok", "postgres": "ok", "redis": "ok"}
	code := http.StatusOK

	if err := h.store.Ping(ctx); err != nil {
		resp["postgres"], resp["status"], code = "down", "degraded", http.StatusServiceUnavailable
	}
	if err := h.queue.Ping(ctx); err != nil {
		resp["redis"], resp["status"], code = "down", "degraded", http.StatusServiceUnavailable
	}
	writeJSON(w, code, resp)
}
