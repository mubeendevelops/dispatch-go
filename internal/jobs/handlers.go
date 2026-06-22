package jobs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// exportPDFDuration is how long the simulated PDF export "works" for.
const exportPDFDuration = 3 * time.Second

// DefaultRegistry returns a Registry with every built-in handler registered.
//
// This is the one obvious place to wire up a new job_type: implement a JobHandler
// and add a single Register line here.
func DefaultRegistry() *Registry {
	r := NewRegistry()
	r.Register("echo", HandlerFunc(echoHandler))
	r.Register("always_fail", HandlerFunc(alwaysFailHandler))
	r.Register("send_email", SendEmailHandler{})
	r.Register("export_pdf", ExportPDFHandler{})
	return r
}

// echoHandler returns the payload unchanged. Useful as a smoke test.
func echoHandler(_ context.Context, payload map[string]interface{}) (interface{}, error) {
	return payload, nil
}

// alwaysFailHandler always errors, so we can watch a job exhaust its retries and
// land in the dead-letter queue.
func alwaysFailHandler(_ context.Context, _ map[string]interface{}) (interface{}, error) {
	return nil, errors.New("always_fail: deliberate failure for testing retries")
}

// SendEmailHandler simulates sending an email and returns a fake message id. It's
// a struct (not a bare func) because a real implementation would hold an
// email-provider client as a field -- this is where that dependency would live.
//
// Payload: {"to": "<address>", "subject": "...", "body": "..."}; only "to" is required.
type SendEmailHandler struct{}

func (SendEmailHandler) Handle(_ context.Context, payload map[string]interface{}) (interface{}, error) {
	to, _ := payload["to"].(string)
	if to == "" {
		return nil, errors.New(`send_email: payload field "to" is required`)
	}
	// Pretend we called an email provider and it accepted the message.
	return map[string]interface{}{
		"message_id": "msg_" + uuid.NewString(),
		"to":         to,
		"status":     "sent",
	}, nil
}

// ExportPDFHandler simulates a slow PDF export and returns a fake URL. Like
// SendEmailHandler it's a struct so a real version could hold a renderer/storage
// client.
//
// Payload: {"document_id": "<id>"} -- optional; a random id is used if omitted.
type ExportPDFHandler struct{}

func (ExportPDFHandler) Handle(ctx context.Context, payload map[string]interface{}) (interface{}, error) {
	// Simulate slow work, but honor cancellation so worker shutdown isn't blocked
	// for the full duration.
	select {
	case <-time.After(exportPDFDuration):
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	docID, _ := payload["document_id"].(string)
	if docID == "" {
		docID = uuid.NewString()
	}
	return map[string]interface{}{
		"document_id": docID,
		"url":         fmt.Sprintf("https://files.example.com/exports/%s.pdf", docID),
		"status":      "ready",
	}, nil
}
