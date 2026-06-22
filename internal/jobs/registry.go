// Package jobs defines the job-handler contract and a registry that maps a
// job_type to the handler that runs it. The worker calls Registry.Dispatch to
// run a job; this package is the single place that knows how each job_type
// behaves, so the worker itself stays generic.
package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

// JobHandler runs one kind of job. payload is the job's decoded JSON payload; the
// returned value is JSON-encoded and stored as the job's result. Returning an
// error marks the attempt as failed, after which the worker retries or
// dead-letters the job per the usual policy.
type JobHandler interface {
	Handle(ctx context.Context, payload map[string]interface{}) (interface{}, error)
}

// HandlerFunc lets a plain function satisfy JobHandler, so trivial handlers don't
// need their own type. (Mirrors net/http's HandlerFunc.)
type HandlerFunc func(ctx context.Context, payload map[string]interface{}) (interface{}, error)

// Handle implements JobHandler.
func (f HandlerFunc) Handle(ctx context.Context, payload map[string]interface{}) (interface{}, error) {
	return f(ctx, payload)
}

// Registry maps a job_type to its handler.
type Registry struct {
	handlers map[string]JobHandler
}

// NewRegistry returns an empty registry. Most callers want DefaultRegistry.
func NewRegistry() *Registry {
	return &Registry{handlers: make(map[string]JobHandler)}
}

// Register binds a job_type to a handler. It panics on a duplicate registration:
// that's a wiring mistake that should fail loudly at startup, not a runtime
// condition. (Registration runs once at boot, never in a processing path, so this
// does not violate the "no panics in request paths" rule.)
func (r *Registry) Register(jobType string, h JobHandler) {
	if _, exists := r.handlers[jobType]; exists {
		panic(fmt.Sprintf("jobs: duplicate handler for job_type %q", jobType))
	}
	r.handlers[jobType] = h
}

// Get returns the handler for jobType, or an error if none is registered.
func (r *Registry) Get(jobType string) (JobHandler, error) {
	h, ok := r.handlers[jobType]
	if !ok {
		return nil, fmt.Errorf("no handler registered for job_type %q", jobType)
	}
	return h, nil
}

// Types returns the registered job_type names, sorted (handy for startup logging).
func (r *Registry) Types() []string {
	types := make([]string, 0, len(r.handlers))
	for t := range r.handlers {
		types = append(types, t)
	}
	sort.Strings(types)
	return types
}

// Dispatch runs a job end to end: find the handler for jobType, decode the raw
// payload into a map, invoke the handler, and JSON-encode its result. Any failure
// -- unknown type, malformed payload, or handler error -- is returned to the
// worker, which applies the retry/dead-letter policy. Keeping the JSON details
// here means the worker never has to know about individual job types.
func (r *Registry) Dispatch(ctx context.Context, jobType string, rawPayload json.RawMessage) (json.RawMessage, error) {
	handler, err := r.Get(jobType)
	if err != nil {
		return nil, err
	}

	payload, err := decodePayload(rawPayload)
	if err != nil {
		return nil, err
	}

	result, err := handler.Handle(ctx, payload)
	if err != nil {
		return nil, err
	}

	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encode result for job_type %q: %w", jobType, err)
	}
	return encoded, nil
}

// decodePayload turns the stored JSON payload into a map. An empty or null
// payload becomes an empty map; a non-object payload is an error.
func decodePayload(raw json.RawMessage) (map[string]interface{}, error) {
	if len(raw) == 0 {
		return map[string]interface{}{}, nil
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("invalid payload (expected a JSON object): %w", err)
	}
	if m == nil { // payload was the literal null
		m = map[string]interface{}{}
	}
	return m, nil
}
