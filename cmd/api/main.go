// Command api serves the HTTP job API.
//
// Run the "migrate" subcommand to apply database migrations, or run with no
// arguments to start the server:
//
//	go run ./cmd/api migrate
//	go run ./cmd/api
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/mubeendevelops/dispatch-go/internal/config"
	"github.com/mubeendevelops/dispatch-go/internal/handlers"
	"github.com/mubeendevelops/dispatch-go/internal/metrics"
	"github.com/mubeendevelops/dispatch-go/internal/queue"
	"github.com/mubeendevelops/dispatch-go/internal/store"
	"github.com/mubeendevelops/dispatch-go/migrations"
)

func main() {
	cfg := config.Load()

	// Subcommand: apply migrations, then exit.
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		runMigrate(cfg)
		return
	}

	if err := runServer(cfg); err != nil {
		log.Fatal(err)
	}
}

func runMigrate(cfg config.Config) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	st, err := store.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("migrate: %v", err)
	}
	defer st.Close()

	if err := st.Migrate(ctx, migrations.Files); err != nil {
		log.Fatalf("migrate: %v", err)
	}
	log.Println("migrations applied")
}

func runServer(cfg config.Config) error {
	// Short timeout just for dependency startup; the server itself runs without one.
	startCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	st, err := store.New(startCtx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()

	q := queue.New(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	defer q.Close()
	if err := q.Ping(startCtx); err != nil {
		return err
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)                 // converts a handler panic into a 500 instead of crashing the process
	r.Use(handlers.CORS(cfg.CORSAllowedOrigin)) // allow the dashboard origin + answer CORS preflight
	r.Mount("/", handlers.New(st, q, cfg.Queues).Routes())

	// Serve Prometheus /metrics from an outer mux so scrapes bypass the chi
	// middleware chain -- no CORS headers or per-request access logs on a target
	// Prometheus hits every few seconds. The gauges (job_queue_depth,
	// workers_active) are read from Redis at scrape time via metricsSource.
	src := metricsSource{q: q, st: st, configured: cfg.Queues}
	mux := http.NewServeMux()
	mux.Handle("/metrics", metrics.NewAPIHandler(src))
	mux.Handle("/", r)

	srv := &http.Server{
		Addr:         ":" + cfg.APIPort,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown: on SIGINT/SIGTERM stop accepting connections and let
	// in-flight requests drain before exiting.
	shutdownErr := make(chan error, 1)
	go func() {
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
		<-stop
		log.Println("shutting down api...")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		shutdownErr <- srv.Shutdown(ctx)
	}()

	log.Printf("api listening on :%s (Prometheus metrics at /metrics)", cfg.APIPort)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return <-shutdownErr
}
