package enqueue

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/mubeendevelops/dispatch-go/internal/models"
)

// TestSubmitRejectsTenantlessJob is the structural guarantee of Phase A: the shared
// producer path refuses to admit a job with no owning tenant. It doubles as proof
// that the guard runs BEFORE persistence -- we construct the Enqueuer with nil
// store and queue, so if Submit ever reached CreateJob it would panic. Getting
// ErrMissingTenant back (not a panic) shows nothing was written.
func TestSubmitRejectsTenantlessJob(t *testing.T) {
	e := New(nil, nil) // store/queue must never be touched for a tenantless job

	job := &models.Job{JobType: "echo"} // TenantID left as uuid.Nil

	err := e.Submit(context.Background(), job)
	if !errors.Is(err, ErrMissingTenant) {
		t.Fatalf("Submit() error = %v, want ErrMissingTenant", err)
	}
}

// TestApplyDefaults pins the defaults every producer relies on. MaxRetries is
// deliberately excluded: 0 is a valid budget a caller may have chosen, so it is set
// explicitly at the call site rather than defaulted here.
func TestApplyDefaults(t *testing.T) {
	job := &models.Job{}
	applyDefaults(job)

	if job.ID == uuid.Nil {
		t.Error("applyDefaults did not assign an ID")
	}
	if job.QueueName != "default" {
		t.Errorf("QueueName = %q, want %q", job.QueueName, "default")
	}
	if string(job.Payload) != "{}" {
		t.Errorf("Payload = %s, want {}", job.Payload)
	}
	if job.Status != models.StatusPending {
		t.Errorf("Status = %q, want %q", job.Status, models.StatusPending)
	}
	if job.MaxRetries != 0 {
		t.Errorf("MaxRetries = %d, want 0 (intentionally not defaulted)", job.MaxRetries)
	}

	// applyDefaults must NOT invent a tenant -- that is the enqueue guard's job.
	if job.TenantID != uuid.Nil {
		t.Errorf("TenantID = %v, want uuid.Nil (never defaulted)", job.TenantID)
	}
}
