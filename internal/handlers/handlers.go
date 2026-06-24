// Package handlers contains the HTTP API: request decoding, validation, and
// consistent JSON responses. Durable state lives in store (Postgres); the work
// signal lives in queue (Redis).
//
// The handlers are split across files by concern: this file holds wiring (struct,
// constructor, routes, health); jobs.go has the /jobs endpoints; admin.go has the
// dashboard/stats endpoints; middleware.go and params.go hold CORS, JSON
// 404/405, and request-parameter validation.
package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/mubeendevelops/dispatch-go/internal/enqueue"
	"github.com/mubeendevelops/dispatch-go/internal/queue"
	"github.com/mubeendevelops/dispatch-go/internal/store"
)

// Handler holds the dependencies the routes need.
type Handler struct {
	store    *store.Store
	queue    *queue.Queue
	enqueuer *enqueue.Enqueuer
	queues   []string // configured queues, surfaced (unioned with DB queues) in stats
}

// New wires up a Handler. queues is the configured queue list (config.Queues),
// used by the admin stats endpoint so known queues always appear even at zero
// depth. The enqueuer is the shared persist-before-enqueue path cmd/scheduler
// also uses.
func New(s *store.Store, q *queue.Queue, queues []string) *Handler {
	return &Handler{store: s, queue: q, enqueuer: enqueue.New(s, q), queues: queues}
}

// Routes returns the API router. It is mounted at "/" by cmd/api.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()

	// Render routing misses as our standard JSON error shape, not chi's plain text.
	r.NotFound(h.notFound)
	r.MethodNotAllowed(h.methodNotAllowed)

	r.Get("/healthz", h.health)

	r.Route("/api/v1", func(r chi.Router) {
		// Jobs.
		r.Get("/jobs", h.listJobs)
		r.Post("/jobs/enqueue", h.enqueueJob)
		r.Get("/jobs/{id}", h.getJob)
		r.Post("/jobs/{id}/retry", h.retryJob)
		r.Post("/jobs/{id}/cancel", h.cancelJob)

		// Admin / dashboard.
		r.Get("/admin/stats", h.stats)
		r.Get("/admin/dashboard", h.dashboard)
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
