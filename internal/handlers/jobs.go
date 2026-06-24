package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/mubeendevelops/dispatch-go/internal/enqueue"
	"github.com/mubeendevelops/dispatch-go/internal/models"
	"github.com/mubeendevelops/dispatch-go/internal/store"
)

// enqueueRequest is the POST /api/v1/jobs/enqueue body.
type enqueueRequest struct {
	QueueName  string          `json:"queue_name"`
	JobType    string          `json:"job_type"`
	Payload    json.RawMessage `json:"payload"`
	MaxRetries *int            `json:"max_retries"` // pointer: distinguishes 0 from "unset"
}

// jobListResponse is the paginated GET /api/v1/jobs shape.
type jobListResponse struct {
	Jobs   []models.Job `json:"jobs"`
	Total  int          `json:"total"`
	Limit  int          `json:"limit"`
	Offset int          `json:"offset"`
}

// listJobs returns a filtered, paginated page of jobs plus the total match count.
func (h *Handler) listJobs(w http.ResponseWriter, r *http.Request) {
	p, err := parseListParams(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	jobs, total, err := h.store.ListJobs(r.Context(), store.JobFilter{
		Queue:  p.Queue,
		Status: p.Status,
		Limit:  p.Limit,
		Offset: p.Offset,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list jobs")
		return
	}
	writeJSON(w, http.StatusOK, jobListResponse{
		Jobs:   jobs,
		Total:  total,
		Limit:  p.Limit,
		Offset: p.Offset,
	})
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
	// failed -- a recoverable state we report distinctly from failing to persist.
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

// retryJob resets a failed job to pending and re-enqueues it.
func (h *Handler) retryJob(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid job id")
		return
	}
	job, err := h.store.RetryJob(r.Context(), id)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrJobNotFound):
			writeError(w, http.StatusNotFound, "job not found")
		case errors.Is(err, store.ErrJobNotRetryable):
			writeError(w, http.StatusConflict, "only failed jobs can be retried")
		default:
			writeError(w, http.StatusInternalServerError, "failed to retry job")
		}
		return
	}
	// State was reset in Postgres first; now put the id back on its work queue
	// (persist-before-enqueue). A failure here leaves a recoverable pending row.
	if err := h.queue.Enqueue(r.Context(), job.QueueName, job.ID.String()); err != nil {
		writeError(w, http.StatusInternalServerError, "job reset but failed to re-enqueue")
		return
	}
	writeJSON(w, http.StatusOK, job)
}

// cancelJob marks a pending job cancelled so it never runs.
func (h *Handler) cancelJob(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid job id")
		return
	}
	job, err := h.store.CancelJob(r.Context(), id)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrJobNotFound):
			writeError(w, http.StatusNotFound, "job not found")
		case errors.Is(err, store.ErrJobNotCancellable):
			writeError(w, http.StatusConflict, "only pending jobs can be cancelled")
		default:
			writeError(w, http.StatusInternalServerError, "failed to cancel job")
		}
		return
	}
	// Best-effort: drop the id from Redis so a worker doesn't waste a pop and the
	// queue depth stays accurate. The worker's atomic claim is the real guarantee
	// a cancelled job never runs, so ignore any error here.
	_ = h.queue.Remove(r.Context(), job.QueueName, job.ID.String())
	writeJSON(w, http.StatusOK, job)
}
