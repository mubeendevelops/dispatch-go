// Package models defines the core domain types shared across the API, worker,
// and scheduler. The Job type maps directly onto the `jobs` table.
package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// DefaultTenantID is the tenant that owns every job created before per-request
// tenancy exists. Migration 0011 backfills all pre-tenancy rows to it, and until
// Phase B wires real auth (API keys / sessions) the API attributes newly enqueued
// jobs to it -- so the external API keeps working unchanged while every job is
// nonetheless owned. It must be non-nil: the enqueue path rejects a uuid.Nil
// ("unowned") tenant as a bug, so there is no valid "empty" owner. Kept in sync
// with the seed in migration 0005. (A const can't hold a uuid.UUID, hence a var.)
var DefaultTenantID = uuid.MustParse("11111111-1111-1111-1111-111111111111")

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
	TenantID     uuid.UUID       `json:"tenant_id"` // owning tenant; isolation is enforced on this in the store
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
