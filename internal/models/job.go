// Package models defines the core domain types shared across the API, worker,
// and scheduler. The Job type maps directly onto the `jobs` table.
package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Status is a job's lifecycle state. It is stored as TEXT in Postgres.
type Status string

const (
	StatusPending    Status = "pending"     // enqueued (or re-enqueued for retry), not yet picked up
	StatusProcessing Status = "processing"  // claimed by a worker, handler running
	StatusCompleted  Status = "completed"   // handler succeeded; result is set
	StatusFailed     Status = "failed"      // gave up after exhausting retries; recorded in dead_letter_queue
	StatusCancelled  Status = "cancelled"   // cancelled via the API while still pending; never runs
	StatusDeadLetter Status = "dead_letter" // reserved: a dead-lettered job is currently marked "failed" + a dead_letter_queue row
)

// AllStatuses lists the statuses a job can actually hold. It drives stable
// dashboard keys and request validation. StatusDeadLetter is intentionally
// excluded: it is reserved/unused today (a dead-lettered job is stored as
// StatusFailed plus a dead_letter_queue row), so surfacing it would only add a
// permanently-zero bucket.
var AllStatuses = []Status{
	StatusPending, StatusProcessing, StatusCompleted, StatusFailed, StatusCancelled,
}

// Valid reports whether s is a status the API accepts (e.g. as a list filter).
func (s Status) Valid() bool {
	for _, v := range AllStatuses {
		if s == v {
			return true
		}
	}
	return false
}

// Job is one unit of work. Payload and Result are kept as raw JSON so the queue
// stays payload-agnostic: each handler decides how to interpret them. Nullable
// columns are pointers so "unset" is distinguishable from a zero value.
type Job struct {
	ID           uuid.UUID       `json:"id"`
	QueueName    string          `json:"queue_name"`
	JobType      string          `json:"job_type"`
	Payload      json.RawMessage `json:"payload"`
	Status       Status          `json:"status"`
	Result       json.RawMessage `json:"result,omitempty"`
	ErrorMessage *string         `json:"error_message,omitempty"`
	RetryCount   int             `json:"retry_count"`
	MaxRetries   int             `json:"max_retries"`
	ScheduledAt  *time.Time      `json:"scheduled_at,omitempty"`
	StartedAt    *time.Time      `json:"started_at,omitempty"`
	CompletedAt  *time.Time      `json:"completed_at,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}
