// Package metrics wires the project's Prometheus instrumentation.
//
// Metrics are split by the process that owns them, because Prometheus is a
// pull-based system: it scrapes one /metrics target per process, and a counter
// or histogram only exists in the memory of the process that increments it.
//
//   - The WORKER owns the job-processing metrics (job_latency_seconds,
//     jobs_processed_total): it's the process that actually runs jobs, so it
//     records them in-process as they happen. See Worker below.
//   - The API owns the infrastructure gauges (job_queue_depth, workers_active):
//     these describe current Redis state, so the API reads them at scrape time.
//     See api.go.
//
// Each process builds its OWN registry and serves it at /metrics. That's why the
// metrics aren't package-level globals auto-registered in an init(): importing
// this package must not register the API's gauges into the worker (or vice
// versa). Constructors hand back a process-specific registry instead.
package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// latencyBuckets spans fast handlers (echo finishes in a few ms) through slow
// ones (export_pdf sleeps ~3s) with headroom to a couple of minutes. Histogram
// buckets are fixed at registration, so we pick a range wide enough that no
// common job type saturates the top bucket -- otherwise its quantiles would all
// read "+Inf" and tell us nothing.
var latencyBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120}

// Worker holds the worker process's metrics and the private registry they're
// exposed on.
type Worker struct {
	reg       *prometheus.Registry
	latency   *prometheus.HistogramVec
	processed *prometheus.CounterVec
}

// NewWorker builds the worker metrics and registers them -- alongside the Go
// runtime and process collectors (goroutines, GC, memory, fds, CPU) -- on a
// fresh registry.
func NewWorker() *Worker {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	latency := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "job_latency_seconds",
		Help:    "Handler execution time per job in seconds, labelled by job_type.",
		Buckets: latencyBuckets,
	}, []string{"job_type"})

	processed := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "jobs_processed_total",
		Help: "Jobs that reached a terminal state, labelled by status (completed or failed).",
	}, []string{"status"})

	reg.MustRegister(latency, processed)

	// Pre-create the known counter series so they report 0 from process start
	// rather than being absent until the first job of that outcome. A continuous
	// series makes rate() and dashboards behave (no gaps, no first-sample spike).
	for _, s := range []string{"completed", "failed"} {
		processed.WithLabelValues(s)
	}

	return &Worker{reg: reg, latency: latency, processed: processed}
}

// ObserveLatency records how long a handler ran for a job_type. It is called for
// every dispatch -- success OR failure -- because a failed attempt still spent
// that time running, and we want latency to reflect real handler cost.
func (w *Worker) ObserveLatency(jobType string, d time.Duration) {
	w.latency.WithLabelValues(jobType).Observe(d.Seconds())
}

// IncProcessed counts a job that reached a terminal state ("completed" or
// "failed"). A scheduled retry is deliberately NOT counted here: the job hasn't
// finished, so counting the attempt would make jobs_processed_total tally
// attempts instead of jobs.
func (w *Worker) IncProcessed(status string) {
	w.processed.WithLabelValues(status).Inc()
}

// Handler serves this worker's registry in the Prometheus text exposition format.
func (w *Worker) Handler() http.Handler {
	return promhttp.HandlerFor(w.reg, promhttp.HandlerOpts{})
}
