package store

import (
	"context"
	"fmt"

	"github.com/mubeendevelops/dispatch-go/internal/models"
)

// Metrics holds the time-windowed counters the admin stats endpoint turns into
// processing-rate, latency, and failure-rate numbers. Windows are trailing (last
// minute / last hour) so the dashboard reflects current health, not all-time
// history.
type Metrics struct {
	CompletedLastMinute int     // drives processing rate (jobs/min)
	CompletedLastHour   int     // denominator for failure rate
	FailedLastHour      int     // numerator for failure rate
	AvgLatencySeconds   float64 // avg(completed_at - started_at) over the last hour; 0 if no samples
}

// JobMetrics computes all the windowed counters in a single pass over the jobs
// table using FILTERed aggregates.
func (s *Store) JobMetrics(ctx context.Context) (Metrics, error) {
	const q = `
		SELECT
			count(*) FILTER (WHERE status = 'completed' AND completed_at >= now() - interval '1 minute'),
			count(*) FILTER (WHERE status = 'completed' AND completed_at >= now() - interval '1 hour'),
			count(*) FILTER (WHERE status = 'failed'    AND completed_at >= now() - interval '1 hour'),
			coalesce(
				avg(extract(epoch FROM (completed_at - started_at)))
				FILTER (WHERE status = 'completed' AND completed_at >= now() - interval '1 hour'
				        AND started_at IS NOT NULL AND completed_at IS NOT NULL),
				0)
		FROM jobs`
	var m Metrics
	if err := s.pool.QueryRow(ctx, q).Scan(
		&m.CompletedLastMinute, &m.CompletedLastHour, &m.FailedLastHour, &m.AvgLatencySeconds,
	); err != nil {
		return Metrics{}, fmt.Errorf("job metrics: %w", err)
	}
	return m, nil
}

// StatusCounts returns the total number of jobs in each status, plus how many
// jobs were created today (server-local day). Both come from one grouped query.
func (s *Store) StatusCounts(ctx context.Context) (map[models.Status]int, int, error) {
	const q = `
		SELECT status,
		       count(*),
		       count(*) FILTER (WHERE created_at >= date_trunc('day', now()))
		FROM jobs
		GROUP BY status`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, 0, fmt.Errorf("status counts: %w", err)
	}
	defer rows.Close()

	counts := make(map[models.Status]int)
	today := 0
	for rows.Next() {
		var st models.Status
		var total, t int
		if err := rows.Scan(&st, &total, &t); err != nil {
			return nil, 0, fmt.Errorf("scan status count: %w", err)
		}
		counts[st] = total
		today += t
	}
	return counts, today, rows.Err()
}
