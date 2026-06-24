// Package store is the Postgres persistence layer. It is the single source of
// truth for job state; the Redis queue (package queue) only ever carries job IDs.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mubeendevelops/dispatch-go/internal/models"
)

// Sentinel errors let handlers map store outcomes onto HTTP status codes without
// knowing any SQL: ErrJobNotFound is a 404, and the "not <state>" errors are 409s
// (the job exists but isn't in a state the operation allows).
var (
	ErrJobNotFound       = errors.New("job not found")
	ErrJobNotClaimable   = errors.New("job is not pending (cannot be claimed)")
	ErrJobNotRetryable   = errors.New("job is not failed (cannot be retried)")
	ErrJobNotCancellable = errors.New("job is not pending (cannot be cancelled)")
)

// Store wraps a pgx connection pool. A pool (not a single conn) is safe for
// concurrent use by the API's request handlers and the worker.
type Store struct {
	pool *pgxpool.Pool
}

// New opens a connection pool to Postgres and verifies it with a ping so startup
// fails fast if the database is unreachable.
func New(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close releases all pooled connections.
func (s *Store) Close() { s.pool.Close() }

// Ping reports whether Postgres is reachable (used by the API health check).
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// jobColumns is the canonical column list/order shared by reads so the INSERT's
// RETURNING and the SELECT scan into scanJob the same way.
const jobColumns = `
	id, queue_name, job_type, payload, status, result, error_message,
	retry_count, max_retries, scheduled_at, started_at, completed_at,
	created_at, updated_at`

// CreateJob inserts a new job and fills the struct from RETURNING (created_at,
// updated_at, defaults). The caller supplies the id (google/uuid) so it can
// enqueue and return that id without a second query.
func (s *Store) CreateJob(ctx context.Context, job *models.Job) error {
	const q = `
		INSERT INTO jobs (id, queue_name, job_type, payload, status, max_retries, scheduled_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING ` + jobColumns
	row := s.pool.QueryRow(ctx, q,
		job.ID, job.QueueName, job.JobType, job.Payload, job.Status, job.MaxRetries, job.ScheduledAt)
	return scanJob(row, job)
}

// GetJob loads a single job by id, returning ErrJobNotFound if it does not exist.
func (s *Store) GetJob(ctx context.Context, id uuid.UUID) (*models.Job, error) {
	const q = `SELECT ` + jobColumns + ` FROM jobs WHERE id = $1`
	var job models.Job
	if err := scanJob(s.pool.QueryRow(ctx, q, id), &job); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrJobNotFound
		}
		return nil, fmt.Errorf("get job: %w", err)
	}
	return &job, nil
}

// ClaimJob atomically transitions a job from pending to processing, stamps
// started_at, and returns the claimed row -- all in one statement. The
// WHERE status = pending guard is the point: if the job was cancelled (or already
// claimed by another worker) it updates no row and ClaimJob returns
// ErrJobNotClaimable, so the worker skips it instead of running it. This is what
// makes POST /cancel effective even after the id is already sitting on the queue.
func (s *Store) ClaimJob(ctx context.Context, id uuid.UUID) (*models.Job, error) {
	const q = `
		UPDATE jobs SET status = $2, started_at = now()
		WHERE id = $1 AND status = $3
		RETURNING ` + jobColumns
	var job models.Job
	if err := scanJob(s.pool.QueryRow(ctx, q, id, models.StatusProcessing, models.StatusPending), &job); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrJobNotClaimable
		}
		return nil, fmt.Errorf("claim job: %w", err)
	}
	return &job, nil
}

// MarkCompleted stores the handler result, clears any prior error, and stamps
// completed_at. (updated_at is bumped automatically by the DB trigger.)
func (s *Store) MarkCompleted(ctx context.Context, id uuid.UUID, result json.RawMessage) error {
	const q = `
		UPDATE jobs
		SET status = $2, result = $3, error_message = NULL, completed_at = now()
		WHERE id = $1`
	_, err := s.pool.Exec(ctx, q, id, models.StatusCompleted, result)
	return err
}

// MarkForRetry records a failed attempt that will be retried later: it bumps
// retry_count, stores the error, and sets the job back to pending. The job stays
// out of the work list until the delayed-queue poller promotes it once its
// backoff elapses (see queue.PromoteDue). retryCount is the new, post-increment
// value, computed by the caller.
func (s *Store) MarkForRetry(ctx context.Context, id uuid.UUID, retryCount int, errMsg string) error {
	const q = `
		UPDATE jobs
		SET status = $2, retry_count = $3, error_message = $4
		WHERE id = $1`
	_, err := s.pool.Exec(ctx, q, id, models.StatusPending, retryCount, errMsg)
	return err
}

// DeadLetter is the terminal failure path: the job has exhausted its retries. It
// marks the job failed AND records it in dead_letter_queue, both inside one
// transaction so the job status and the DLQ audit row can never diverge. The
// INSERT is idempotent (ON CONFLICT) so re-running the operation is safe.
func (s *Store) DeadLetter(ctx context.Context, id uuid.UUID, attempts int, reason string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) // no-op after a successful Commit

	if _, err := tx.Exec(ctx, `
		UPDATE jobs
		SET status = $2, retry_count = $3, error_message = $4, completed_at = now()
		WHERE id = $1`,
		id, models.StatusFailed, attempts, reason); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO dead_letter_queue (job_id, reason, attempts)
		VALUES ($1, $2, $3)
		ON CONFLICT (job_id) DO UPDATE
		SET reason = EXCLUDED.reason, attempts = EXCLUDED.attempts`,
		id, reason, attempts); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// scanJob reads one row into job using the jobColumns order.
func scanJob(row pgx.Row, job *models.Job) error {
	return row.Scan(
		&job.ID, &job.QueueName, &job.JobType, &job.Payload, &job.Status, &job.Result,
		&job.ErrorMessage, &job.RetryCount, &job.MaxRetries, &job.ScheduledAt,
		&job.StartedAt, &job.CompletedAt, &job.CreatedAt, &job.UpdatedAt,
	)
}
