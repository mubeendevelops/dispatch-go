// Package enqueue centralizes the one operation the API and the scheduler share:
// admitting a new job into the system. Both producers must follow the same
// "persist to Postgres, THEN push the id to Redis" sequence (see CLAUDE.md), so
// that rule lives here exactly once instead of being copied into every caller.
//
// It sits one layer above store (Postgres) and queue (Redis) and depends on both;
// store and queue stay unaware of each other.
package enqueue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/mubeendevelops/dispatch-go/internal/models"
	"github.com/mubeendevelops/dispatch-go/internal/queue"
	"github.com/mubeendevelops/dispatch-go/internal/store"
)

// DefaultMaxRetries applies when a producer does not specify its own retry budget.
// It matches the API's historical default so scheduled and API-submitted jobs
// behave identically.
const DefaultMaxRetries = 3

// ErrEnqueueAfterPersist means the job row was written to Postgres but pushing
// its id to Redis afterwards failed. The job is therefore durable but not yet
// queued -- a recoverable state, never a lost job. Callers can use errors.Is to
// tell this apart from a plain persist failure (where nothing was written at all).
var ErrEnqueueAfterPersist = errors.New("job persisted but enqueue failed")

// Enqueuer persists jobs and pushes their ids onto the work queue. It wraps the
// two lower layers so the persist-before-enqueue invariant has a single
// implementation shared by every producer.
type Enqueuer struct {
	store *store.Store
	queue *queue.Queue
}

// New wires an Enqueuer from the store and queue.
func New(s *store.Store, q *queue.Queue) *Enqueuer {
	return &Enqueuer{store: s, queue: q}
}

// Submit fills in defaults, persists the job to Postgres, then enqueues its id.
// On success job is updated in place from the inserted row (id, timestamps,
// applied defaults), so the caller can return it straight to a client.
//
// Order is the project's core durability rule: we write the row BEFORE touching
// Redis, so a crash in between can only leave a recoverable row, never a queued
// id that points at nothing. If the persist succeeds but the enqueue fails,
// Submit returns ErrEnqueueAfterPersist so the caller can handle that specific
// (recoverable) case distinctly.
func (e *Enqueuer) Submit(ctx context.Context, job *models.Job) error {
	applyDefaults(job)

	if err := e.store.CreateJob(ctx, job); err != nil {
		return fmt.Errorf("persist job: %w", err)
	}
	if err := e.queue.Enqueue(ctx, job.QueueName, job.ID.String()); err != nil {
		return fmt.Errorf("%w: %v", ErrEnqueueAfterPersist, err)
	}
	return nil
}

// applyDefaults fills the optional fields a producer may leave unset, so every
// entry point creates jobs the same way. MaxRetries is intentionally NOT defaulted
// here: 0 is a valid budget the caller may have chosen deliberately, so callers
// set it explicitly (using DefaultMaxRetries when they want the standard value).
func applyDefaults(job *models.Job) {
	if job.ID == uuid.Nil {
		job.ID = uuid.New()
	}
	if job.QueueName == "" {
		job.QueueName = "default"
	}
	if len(job.Payload) == 0 {
		job.Payload = json.RawMessage(`{}`)
	}
	if job.Status == "" {
		job.Status = models.StatusPending
	}
}
