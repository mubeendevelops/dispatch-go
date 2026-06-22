// Package handlers contains the HTTP API: request decoding, validation, and
// consistent JSON responses. Durable state lives in store (Postgres); the work
// signal lives in queue (Redis).
package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/mubeendevelops/dispatch-go/internal/enqueue"
	"github.com/mubeendevelops/dispatch-go/internal/models"
	"github.com/mubeendevelops/dispatch-go/internal/queue"
	"github.com/mubeendevelops/dispatch-go/internal/store"
)

// Handler holds the dependencies the routes need.
type Handler struct {
	store    *store.Store
	queue    *queue.Queue
	enqueuer *enqueue.Enqueuer
}

// New wires up a Handler. The enqueuer is the shared persist-before-enqueue path,
// the same one cmd/scheduler uses, so jobs enter the system identically however
// they are produced.
func New(s *store.Store, q *queue.Queue) *Handler {
	return &Handler{store: s, queue: q, enqueuer: enqueue.New(s, q)}
}

// Routes returns the API router. It is mounted at "/" by cmd/api.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Get("/healthz", h.health)
	r.Route("/api/v1/jobs", func(r chi.Router) {
		r.Post("/enqueue", h.enqueueJob)
		r.Get("/{id}", h.getJob)
	})
	return r
}

// enqueueRequest is the POST /api/v1/jobs/enqueue body.
type enqueueRequest struct {
	QueueName  string          `json:"queue_name"`
	JobType    string          `json:"job_type"`
	Payload    json.RawMessage `json:"payload"`
	MaxRetries *int            `json:"max_retries"` // pointer: distinguishes 0 from "unset"
}

// enqueueJob persists a job, then enqueues its id, and returns the job immediately.
func (h *Handler) enqueueJob(w http.ResponseWriter, r *http.Request) {
	var req enqueueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.JobType == "" {
		writeError(w, http.StatusBadRequest, "job_type is required")
		return
	}

	// max_retries is the one default the shared enqueuer can't infer for us: 0 is a
	// valid budget a caller may choose deliberately, so "unset" (nil) is resolved
	// here. The remaining defaults (id, queue_name, payload, status) are applied by
	// the enqueuer so every producer fills them identically.
	maxRetries := enqueue.DefaultMaxRetries
	if req.MaxRetries != nil {
		maxRetries = *req.MaxRetries
	}

	job := &models.Job{
		QueueName:  req.QueueName,
		JobType:    req.JobType,
		Payload:    req.Payload,
		MaxRetries: maxRetries,
	}

	// Persist BEFORE enqueueing (CLAUDE.md), via the shared producer path. A
	// returned ErrEnqueueAfterPersist means the row is durable but the Redis push
	// failed -- a recoverable state we report distinctly from failing to persist
	// at all. A reconciler can later re-enqueue such rows.
	if err := h.enqueuer.Submit(r.Context(), job); err != nil {
		if errors.Is(err, enqueue.ErrEnqueueAfterPersist) {
			writeError(w, http.StatusInternalServerError, "job persisted but failed to enqueue")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to persist job")
		return
	}

	// 202 Accepted: the work is queued, not finished. The caller polls GET for the result.
	writeJSON(w, http.StatusAccepted, job)
}

// getJob returns a single job by id.
func (h *Handler) getJob(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid job id")
		return
	}

	job, err := h.store.GetJob(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrJobNotFound) {
			writeError(w, http.StatusNotFound, "job not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load job")
		return
	}
	writeJSON(w, http.StatusOK, job)
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
