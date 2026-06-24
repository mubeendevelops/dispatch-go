package handlers

import (
	"math"
	"net/http"
	"sort"

	"github.com/mubeendevelops/dispatch-go/internal/models"
	"github.com/mubeendevelops/dispatch-go/internal/store"
)

// recentJobsLimit is how many most-recent jobs the dashboard returns.
const recentJobsLimit = 10

// queueDepth is one queue's backlog: ids waiting on the work list, and ids parked
// in the delayed (retry) set.
type queueDepth struct {
	Queue   string `json:"queue"`
	Depth   int64  `json:"depth"`
	Delayed int64  `json:"delayed"`
}

// statsResponse is GET /api/v1/admin/stats.
type statsResponse struct {
	Queues               []queueDepth `json:"queues"`
	ProcessingRatePerMin int          `json:"processing_rate_per_min"`
	AvgLatencyMs         float64      `json:"avg_latency_ms"`
	FailureRate          float64      `json:"failure_rate"` // 0..1 over the last hour
	ActiveWorkers        int64        `json:"active_workers"`
}

// dashboardResponse is GET /api/v1/admin/dashboard.
type dashboardResponse struct {
	TotalsByStatus map[string]int `json:"totals_by_status"`
	JobsToday      int            `json:"jobs_today"`
	RecentJobs     []models.Job   `json:"recent_jobs"`
}

// stats reports per-queue depth, throughput, latency, failure rate, and the
// active worker count -- the numbers a dashboard's top-line cards need.
func (h *Handler) stats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Queues to report: configured ones unioned with any seen in the DB, so depth
	// shows for known queues (even at zero) and for ad-hoc queues that got jobs.
	dbQueues, err := h.store.QueueNames(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load queues")
		return
	}
	depths := make([]queueDepth, 0)
	for _, name := range mergeUnique(h.queues, dbQueues) {
		depth, err := h.queue.Len(ctx, name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read queue depth")
			return
		}
		delayed, err := h.queue.DelayedLen(ctx, name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read delayed depth")
			return
		}
		depths = append(depths, queueDepth{Queue: name, Depth: depth, Delayed: delayed})
	}

	m, err := h.store.JobMetrics(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load metrics")
		return
	}
	workers, err := h.queue.CountActiveWorkers(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to count workers")
		return
	}

	// Failure rate over the last hour: failed / (failed + completed), 0 if neither.
	failureRate := 0.0
	if terminal := m.CompletedLastHour + m.FailedLastHour; terminal > 0 {
		failureRate = float64(m.FailedLastHour) / float64(terminal)
	}

	writeJSON(w, http.StatusOK, statsResponse{
		Queues:               depths,
		ProcessingRatePerMin: m.CompletedLastMinute,
		AvgLatencyMs:         round(m.AvgLatencySeconds*1000, 2),
		FailureRate:          round(failureRate, 4),
		ActiveWorkers:        workers,
	})
}

// dashboard reports totals by status, today's job count, and the most recent jobs.
func (h *Handler) dashboard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	counts, today, err := h.store.StatusCounts(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load status counts")
		return
	}
	// Stable keys: every known status present (zero if absent) so the frontend can
	// rely on the shape.
	totals := make(map[string]int, len(models.AllStatuses))
	for _, st := range models.AllStatuses {
		totals[string(st)] = counts[st]
	}

	recent, _, err := h.store.ListJobs(ctx, store.JobFilter{Limit: recentJobsLimit})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load recent jobs")
		return
	}

	writeJSON(w, http.StatusOK, dashboardResponse{
		TotalsByStatus: totals,
		JobsToday:      today,
		RecentJobs:     recent,
	})
}

// mergeUnique returns the unique, non-empty values of a and b, sorted.
func mergeUnique(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, list := range [][]string{a, b} {
		for _, v := range list {
			if v != "" && !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	sort.Strings(out)
	return out
}

// round rounds f to the given number of decimal places, for tidy JSON numbers.
func round(f float64, places int) float64 {
	pow := math.Pow(10, float64(places))
	return math.Round(f*pow) / pow
}
