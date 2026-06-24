package main

import (
	"context"

	"github.com/mubeendevelops/dispatch-go/internal/queue"
	"github.com/mubeendevelops/dispatch-go/internal/store"
)

// metricsSource adapts the queue + store to metrics.Source, supplying the live
// values the API's Prometheus gauges read on each scrape. It reports depth for
// the configured queues unioned with any seen in the DB -- the same set
// /admin/stats covers -- so the metric matches the JSON endpoint.
type metricsSource struct {
	q          *queue.Queue
	st         *store.Store
	configured []string
}

// QueueDepths reads each queue's Redis work-list length.
func (s metricsSource) QueueDepths(ctx context.Context) (map[string]int64, error) {
	dbQueues, err := s.st.QueueNames(ctx)
	if err != nil {
		return nil, err
	}
	depths := make(map[string]int64)
	for _, name := range mergeQueues(s.configured, dbQueues) {
		n, err := s.q.Len(ctx, name)
		if err != nil {
			return nil, err
		}
		depths[name] = n
	}
	return depths, nil
}

// ActiveWorkers returns the live worker count from the heartbeat keys.
func (s metricsSource) ActiveWorkers(ctx context.Context) (int64, error) {
	return s.q.CountActiveWorkers(ctx)
}

// mergeQueues returns the unique, non-empty names across both lists (order
// doesn't matter -- the caller keys them by queue label).
func mergeQueues(a, b []string) []string {
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
	return out
}
