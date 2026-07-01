package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/mubeendevelops/dispatch-go/internal/models"
)

// scheduleColumns is the canonical column list/order shared by job_schedules
// reads so every SELECT scans the same way. tenant_id travels with each schedule
// so the scheduler can stamp the fired job with its owning tenant.
const scheduleColumns = `
	id, tenant_id, job_type, payload, cron_expression, enabled,
	last_run_at, next_run_at, created_at`

// DueSchedules returns the enabled schedules whose next_run_at is at or before
// now -- the schedules that should fire this tick. Ordered by next_run_at so the
// most overdue ones fire first. The partial index idx_job_schedules_due serves
// this query.
//
// This is deliberately NOT tenant-scoped: like the worker, the scheduler is
// trusted infrastructure that fires every tenant's due schedules. Each returned
// row carries its own tenant_id, which the scheduler stamps onto the job it
// enqueues -- so tenancy is preserved without the due-scan needing a tenant arg.
func (s *Store) DueSchedules(ctx context.Context, now time.Time) ([]models.JobSchedule, error) {
	const q = `
		SELECT ` + scheduleColumns + `
		FROM job_schedules
		WHERE enabled AND next_run_at <= $1
		ORDER BY next_run_at`
	rows, err := s.pool.Query(ctx, q, now)
	if err != nil {
		return nil, fmt.Errorf("query due schedules: %w", err)
	}
	defer rows.Close()

	var out []models.JobSchedule
	for rows.Next() {
		var sc models.JobSchedule
		if err := rows.Scan(
			&sc.ID, &sc.TenantID, &sc.JobType, &sc.Payload, &sc.CronExpression, &sc.Enabled,
			&sc.LastRunAt, &sc.NextRunAt, &sc.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan schedule: %w", err)
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

// ClaimScheduleRun advances a schedule's cursor -- setting last_run_at and
// next_run_at -- but ONLY if next_run_at still equals expectedNextRunAt (the
// value the caller read a moment ago). This compare-and-swap is what makes firing
// safe in two ways:
//
//   - Restart safety. The scheduler advances the cursor BEFORE enqueuing the job.
//     A process that dies after this update never re-fires the schedule, because
//     next_run_at has already moved into the future.
//   - Multiple schedulers. If two schedulers both see the same due row, both call
//     this with the same expectedNextRunAt. Postgres serializes the UPDATEs, so
//     the first moves the cursor and the second matches zero rows. Exactly one
//     scheduler wins the claim and goes on to enqueue the job.
//
// Comparing on the exact value we just read is reliable because we are matching a
// stored timestamp against itself -- no precision drift from a value we computed.
//
// It returns true if this caller won the claim (one row updated), false if a
// concurrent claim got there first or the schedule was disabled in the meantime.
func (s *Store) ClaimScheduleRun(ctx context.Context, id uuid.UUID, expectedNextRunAt, lastRunAt, nextRunAt time.Time) (bool, error) {
	const q = `
		UPDATE job_schedules
		SET last_run_at = $3, next_run_at = $4
		WHERE id = $1 AND enabled AND next_run_at = $2`
	tag, err := s.pool.Exec(ctx, q, id, expectedNextRunAt, lastRunAt, nextRunAt)
	if err != nil {
		return false, fmt.Errorf("claim schedule run: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}
