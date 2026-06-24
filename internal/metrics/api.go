package metrics

import (
	"context"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// collectTimeout bounds the Redis reads a single scrape triggers, so a slow or
// blocked Redis can't hang the /metrics response (and stall Prometheus) forever.
const collectTimeout = 5 * time.Second

// Source supplies the live values the API-side gauges read on each scrape. It's
// an interface so the collector stays decoupled from the queue/store wiring
// (cmd/api implements it) and is trivial to fake in a test.
type Source interface {
	// QueueDepths returns each queue's Redis work-list length (job_queue_depth).
	QueueDepths(ctx context.Context) (map[string]int64, error)
	// ActiveWorkers returns the live worker count from heartbeats (workers_active).
	ActiveWorkers(ctx context.Context) (int64, error)
}

// apiCollector exposes job_queue_depth and workers_active as gauges that are
// read FRESH on every scrape via the Source.
//
// Why a custom Collector instead of plain gauges we Set() on a timer? These
// values live in Redis, not in this process. A scrape-time collector reads them
// exactly when Prometheus asks, so there's no background loop to maintain and no
// window where the gauge is stale relative to Redis. The Collector interface is
// Prometheus's intended hook for exactly this "expose external state" case.
type apiCollector struct {
	src         Source
	depthDesc   *prometheus.Desc
	workersDesc *prometheus.Desc
}

func newAPICollector(src Source) *apiCollector {
	return &apiCollector{
		src: src,
		depthDesc: prometheus.NewDesc(
			"job_queue_depth",
			"Number of job ids waiting on a queue's Redis work list.",
			[]string{"queue"}, nil,
		),
		workersDesc: prometheus.NewDesc(
			"workers_active",
			"Workers that have heartbeated within their TTL.",
			nil, nil,
		),
	}
}

// Describe sends the static descriptors. Implementing it (rather than relying on
// the unchecked path) lets the registry catch a duplicate/misconfigured metric
// at registration time.
func (c *apiCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.depthDesc
	ch <- c.workersDesc
}

// Collect reads the current values from Redis and emits one gauge sample each.
// On error we emit an invalid metric for that descriptor: Prometheus then marks
// the scrape as partially failed instead of us silently reporting a wrong (zero)
// value.
func (c *apiCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), collectTimeout)
	defer cancel()

	if depths, err := c.src.QueueDepths(ctx); err != nil {
		ch <- prometheus.NewInvalidMetric(c.depthDesc, err)
	} else {
		for queue, depth := range depths {
			ch <- prometheus.MustNewConstMetric(c.depthDesc, prometheus.GaugeValue, float64(depth), queue)
		}
	}

	if n, err := c.src.ActiveWorkers(ctx); err != nil {
		ch <- prometheus.NewInvalidMetric(c.workersDesc, err)
	} else {
		ch <- prometheus.MustNewConstMetric(c.workersDesc, prometheus.GaugeValue, float64(n))
	}
}

// NewAPIHandler builds the API's /metrics handler: the Redis-backed gauges above
// plus the standard Go runtime and process collectors, on the API's own registry.
func NewAPIHandler(src Source) http.Handler {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		newAPICollector(src),
	)
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
}
