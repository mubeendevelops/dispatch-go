package handlers

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/mubeendevelops/dispatch-go/internal/models"
)

const (
	defaultListLimit = 20
	maxListLimit     = 100
	maxQueueNameLen  = 100
)

// listParams is the validated GET /api/v1/jobs query string.
type listParams struct {
	Queue  string
	Status string
	Limit  int
	Offset int
}

// parseListParams validates and defaults the list query parameters. It returns a
// plain error whose message is safe to hand straight back to the client as a 400.
func parseListParams(r *http.Request) (listParams, error) {
	q := r.URL.Query()
	p := listParams{Limit: defaultListLimit, Offset: 0}

	if v := q.Get("queue"); v != "" {
		if len(v) > maxQueueNameLen {
			return p, fmt.Errorf("queue must be at most %d characters", maxQueueNameLen)
		}
		p.Queue = v
	}
	if v := q.Get("status"); v != "" {
		if !models.Status(v).Valid() {
			return p, fmt.Errorf("invalid status %q (allowed: %s)", v, strings.Join(statusNames(), ", "))
		}
		p.Status = v
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return p, fmt.Errorf("limit must be an integer")
		}
		if n < 1 || n > maxListLimit {
			return p, fmt.Errorf("limit must be between 1 and %d", maxListLimit)
		}
		p.Limit = n
	}
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return p, fmt.Errorf("offset must be an integer")
		}
		if n < 0 {
			return p, fmt.Errorf("offset must be >= 0")
		}
		p.Offset = n
	}
	return p, nil
}

// statusNames returns the valid status strings, for validation error messages.
func statusNames() []string {
	out := make([]string, len(models.AllStatuses))
	for i, s := range models.AllStatuses {
		out[i] = string(s)
	}
	return out
}
