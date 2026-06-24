package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/mubeendevelops/dispatch-go/internal/models"
)

// JobFilter is the set of optional filters + pagination for ListJobs. An empty
// Queue or Status means "don't filter on it".
type JobFilter struct {
	Queue  string
	Status string
	Limit  int
	Offset int
}

// ListJobs returns a page of jobs (newest first) matching the filter, plus the
// total number of matching rows for pagination. The count and the page are two
// separate queries on purpose: a window-function count would vanish on an
// out-of-range page (zero rows -> no count to read), and for a dashboard a hair
// of inconsistency between the two under concurrent writes is acceptable.
func (s *Store) ListJobs(ctx context.Context, f JobFilter) ([]models.Job, int, error) {
	// Build the shared WHERE clause from whichever filters are set, collecting
	// positional args so values are always parameterized, never interpolated.
	var conds []string
	var args []any
	if f.Queue != "" {
		args = append(args, f.Queue)
		conds = append(conds, fmt.Sprintf("queue_name = $%d", len(args)))
	}
	if f.Status != "" {
		args = append(args, f.Status)
		conds = append(conds, fmt.Sprintf("status = $%d", len(args)))
	}
	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}

	var total int
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM jobs "+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count jobs: %w", err)
	}

	// Append limit/offset as the final two positional args.
	args = append(args, f.Limit, f.Offset)
	listQ := "SELECT " + jobColumns + " FROM jobs " + where +
		fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args))

	rows, err := s.pool.Query(ctx, listQ, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list jobs: %w", err)
	}
	defer rows.Close()

	jobs := make([]models.Job, 0, f.Limit) // non-nil so an empty page marshals to []
	for rows.Next() {
		var job models.Job
		if err := scanJob(rows, &job); err != nil {
			return nil, 0, fmt.Errorf("scan job: %w", err)
		}
		jobs = append(jobs, job)
	}
	return jobs, total, rows.Err()
}

// QueueNames returns the distinct queue names that appear in the jobs table,
// sorted. The stats endpoint unions these with the configured queues so depth
// shows for both known and ad-hoc queues.
func (s *Store) QueueNames(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT queue_name FROM jobs ORDER BY queue_name`)
	if err != nil {
		return nil, fmt.Errorf("queue names: %w", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, fmt.Errorf("scan queue name: %w", err)
		}
		names = append(names, n)
	}
	return names, rows.Err()
}

// RetryJob resets a failed job so it can run again: status back to pending,
// retry_count cleared, and the failure metadata (error, result, timestamps)
// wiped. It also deletes the job's dead_letter_queue row, all in one transaction
// so the job and its DLQ audit row can't diverge. The caller re-enqueues the id
// afterwards (persist-before-enqueue). SELECT ... FOR UPDATE locks the row so a
// concurrent retry or worker claim can't race the state check.
func (s *Store) RetryJob(ctx context.Context, id uuid.UUID) (*models.Job, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) // no-op after a successful Commit

	var status models.Status
	err = tx.QueryRow(ctx, `SELECT status FROM jobs WHERE id = $1 FOR UPDATE`, id).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrJobNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load job for retry: %w", err)
	}
	if status != models.StatusFailed {
		return nil, ErrJobNotRetryable
	}

	var job models.Job
	err = scanJob(tx.QueryRow(ctx, `
		UPDATE jobs
		SET status = $2, retry_count = 0, error_message = NULL, result = NULL,
		    started_at = NULL, completed_at = NULL
		WHERE id = $1
		RETURNING `+jobColumns, id, models.StatusPending), &job)
	if err != nil {
		return nil, fmt.Errorf("reset job for retry: %w", err)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM dead_letter_queue WHERE job_id = $1`, id); err != nil {
		return nil, fmt.Errorf("clear dead-letter row: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &job, nil
}

// CancelJob marks a pending job cancelled (a terminal state); completed_at doubles
// as the "ended at" stamp, as it does for failed jobs. Only pending jobs can be
// cancelled -- a processing job is already in flight and isn't preempted -- so the
// UPDATE is guarded by status = pending. When no row changes we disambiguate a
// missing job (404) from a wrong-state job (409) for the caller.
func (s *Store) CancelJob(ctx context.Context, id uuid.UUID) (*models.Job, error) {
	var job models.Job
	err := scanJob(s.pool.QueryRow(ctx, `
		UPDATE jobs SET status = $2, completed_at = now()
		WHERE id = $1 AND status = $3
		RETURNING `+jobColumns, id, models.StatusCancelled, models.StatusPending), &job)
	if errors.Is(err, pgx.ErrNoRows) {
		exists, e := s.jobExists(ctx, id)
		if e != nil {
			return nil, e
		}
		if !exists {
			return nil, ErrJobNotFound
		}
		return nil, ErrJobNotCancellable
	}
	if err != nil {
		return nil, fmt.Errorf("cancel job: %w", err)
	}
	return &job, nil
}

// jobExists reports whether a job row with the id exists.
func (s *Store) jobExists(ctx context.Context, id uuid.UUID) (bool, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM jobs WHERE id = $1)`, id).Scan(&exists); err != nil {
		return false, fmt.Errorf("check job exists: %w", err)
	}
	return exists, nil
}
