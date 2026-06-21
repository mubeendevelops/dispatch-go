// Command worker consumes job ids from Redis, runs the matching handler, and
// writes the outcome back to Postgres.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/mubeendevelops/dispatch-go/internal/config"
	"github.com/mubeendevelops/dispatch-go/internal/models"
	"github.com/mubeendevelops/dispatch-go/internal/queue"
	"github.com/mubeendevelops/dispatch-go/internal/store"
)

// brpopTimeout bounds each blocking pop so the worker notices a shutdown signal
// between attempts rather than blocking forever.
const brpopTimeout = 5 * time.Second

func main() {
	cfg := config.Load()

	// Root context is cancelled on SIGINT/SIGTERM to unwind the run loop cleanly.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
		<-stop
		log.Println("shutting down worker...")
		cancel()
	}()

	st, err := store.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("worker: %v", err)
	}
	defer st.Close()

	q := queue.New(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	defer q.Close()
	if err := q.Ping(ctx); err != nil {
		log.Fatalf("worker: redis: %v", err)
	}

	log.Printf("worker watching queues %v", cfg.Queues)
	run(ctx, st, q, cfg.Queues)
	log.Println("worker stopped")
}

// run is the consume loop: block for a job id, process it, repeat until shutdown.
func run(ctx context.Context, st *store.Store, q *queue.Queue, queues []string) {
	for {
		if ctx.Err() != nil {
			return
		}

		jobID, queueName, err := q.Dequeue(ctx, brpopTimeout, queues...)
		if err != nil {
			switch {
			case errors.Is(err, redis.Nil):
				continue // timed out with no work; loop and block again
			case ctx.Err() != nil:
				return // shutdown interrupted the blocking pop
			default:
				log.Printf("dequeue: %v", err)
				time.Sleep(time.Second) // back off so a Redis blip doesn't spin the CPU
				continue
			}
		}

		if err := process(ctx, st, jobID); err != nil {
			log.Printf("job %s (queue %s): %v", jobID, queueName, err)
		}
	}
}

// process loads the job, marks it processing, runs its handler, and records the result.
func process(ctx context.Context, st *store.Store, jobID string) error {
	id, err := uuid.Parse(jobID)
	if err != nil {
		return err // a malformed id maps to no row; nothing to do but drop it
	}

	job, err := st.GetJob(ctx, id)
	if err != nil {
		return err
	}
	if err := st.MarkProcessing(ctx, id); err != nil {
		return err
	}

	result, handlerErr := handle(job)
	if handlerErr != nil {
		// Retry/backoff/dead-letter handling arrives in a later step; for now we
		// just record the failure.
		return st.MarkFailed(ctx, id, handlerErr.Error())
	}

	log.Printf("job %s completed (type=%s)", id, job.JobType)
	return st.MarkCompleted(ctx, id, result)
}

// handle is the stand-in for the handler registry (a later step). Every job type
// currently runs the echo handler: it returns the job's payload unchanged.
func handle(job *models.Job) (json.RawMessage, error) {
	if len(job.Payload) == 0 {
		return json.RawMessage(`{}`), nil
	}
	return job.Payload, nil
}
