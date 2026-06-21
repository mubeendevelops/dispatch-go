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

	"github.com/mubeendevelops/dispatch-go/internal/models"
	"github.com/mubeendevelops/dispatch-go/internal/queue"
	"github.com/mubeendevelops/dispatch-go/internal/store"
)

// defaultMaxRetries applies when the caller omits max_retries.
const defaultMaxRetries = 3

// Handler holds the dependencies the routes need.
type Handler struct {
	store *store.Store
	queue *queue.Queue
}

// New wires up a Handler.
func New(s *store.Store, q *queue.Queue) *Handler {
	return &Handler{store: s, queue: q}
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

	// Apply defaults.
	if req.QueueName == "" {
		req.QueueName = "default"
	}
	if len(req.Payload) == 0 {
		req.Payload = json.RawMessage(`{}`)
	}
	maxRetries := defaultMaxRetries
	if req.MaxRetries != nil {
		maxRetries = *req.MaxRetries
	}

	job := &models.Job{
		ID:         uuid.New(),
		QueueName:  req.QueueName,
		JobType:    req.JobType,
		Payload:    req.Payload,
		Status:     models.StatusPending,
		MaxRetries: maxRetries,
	}

	// Persist BEFORE enqueueing (CLAUDE.md): Postgres is the source of truth, so a
	// crash after the insert but before the LPUSH leaves a recoverable row -- never
	// a queued id that points at no row.
	if err := h.store.CreateJob(r.Context(), job); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to persist job")
		return
	}
	if err := h.queue.Enqueue(r.Context(), job.QueueName, job.ID.String()); err != nil {
		// The row exists but isn't queued. Surface it; a re-enqueue/reconciler path
		// can recover these rows in a later step.
		writeError(w, http.StatusInternalServerError, "job persisted but failed to enqueue")
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
