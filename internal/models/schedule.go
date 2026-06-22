package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// JobSchedule is a recurring job definition: a cron expression plus the template
// (job_type + payload) used to enqueue a job each time it fires. It maps onto the
// job_schedules table.
//
// The scheduler reads enabled rows whose NextRunAt has passed, enqueues a job
// from the template, and advances NextRunAt to the cron expression's next time.
// NextRunAt is the durable cursor that makes the scheduler restart-safe: it lives
// in Postgres, not in scheduler memory, so a restarted (or replacement) scheduler
// resumes exactly where the previous one left off.
type JobSchedule struct {
	ID             uuid.UUID       `json:"id"`
	JobType        string          `json:"job_type"`
	Payload        json.RawMessage `json:"payload"`
	CronExpression string          `json:"cron_expression"`
	Enabled        bool            `json:"enabled"`
	LastRunAt      *time.Time      `json:"last_run_at,omitempty"` // nil until the first fire
	NextRunAt      time.Time       `json:"next_run_at"`           // NOT NULL: always the next due time
	CreatedAt      time.Time       `json:"created_at"`
}
