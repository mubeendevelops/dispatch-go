// Command worker consumes job ids from Redis, runs the matching handler, and
// writes the outcome back to Postgres. Alongside the consume loop it runs a
// poller that promotes due delayed jobs (retry backoff) back onto the work list.
package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/mubeendevelops/dispatch-go/internal/config"
	"github.com/mubeendevelops/dispatch-go/internal/jobs"
	"github.com/mubeendevelops/dispatch-go/internal/models"
	"github.com/mubeendevelops/dispatch-go/internal/queue"
	"github.com/mubeendevelops/dispatch-go/internal/store"
)

const (
	// brpopTimeout bounds each blocking pop so the worker notices a shutdown
	// signal between attempts rather than blocking forever.
	brpopTimeout = 5 * time.Second

	// pollInterval is how often the delayed-queue poller checks for jobs whose
	// backoff has elapsed. It is shorter than the smallest backoff (2s) so a due
	// retry waits at most ~1s extra; a smaller value cuts latency at the cost of
	// more Redis round-trips.
	pollInterval = 1 * time.Second

	// promoteBatch caps how many due jobs a single poll moves per queue, so a
	// large backlog drains over several ticks instead of one oversized command.
	promoteBatch = 100

	// maxBackoff caps the exponential delay. 2^attempts seconds grows extremely
	// fast, so without a ceiling a large max_retries would schedule an absurd --
	// and eventually int64-overflowing -- wait.
	maxBackoff = 1 * time.Hour
)

func main() {
	cfg := config.Load()

	// Root context is cancelled on SIGINT/SIGTERM to unwind both loops cleanly.
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

	// The registry maps job_type -> handler. All handlers are wired up in
	// jobs.DefaultRegistry; the worker below stays generic.
	registry := jobs.DefaultRegistry()

	// A stable id for this worker process, used for the liveness heartbeats the
	// admin stats endpoint counts as "active workers".
	workerID := uuid.NewString()

	log.Printf("worker %s watching queues %v", workerID, cfg.Queues)
	log.Printf("registered job types: %v", registry.Types())

	// Run the consume loop, the delayed-queue poller, and the heartbeat side by
	// side; all unwind when ctx is cancelled, and wg.Wait keeps main alive until
	// they do.
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); consume(ctx, st, q, registry, cfg.Queues) }()
	go func() { defer wg.Done(); poll(ctx, q, cfg.Queues) }()
	go func() { defer wg.Done(); heartbeat(ctx, q, workerID) }()
	wg.Wait()

	log.Println("worker stopped")
}

// consume is the work loop: block for a job id, process it, repeat until shutdown.
func consume(ctx context.Context, st *store.Store, q *queue.Queue, registry *jobs.Registry, queues []string) {
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

		if err := process(ctx, st, q, registry, jobID); err != nil {
			log.Printf("job %s (queue %s): %v", jobID, queueName, err)
		}
	}
}

// poll promotes due delayed jobs (retries whose backoff has elapsed) back onto
// their work lists, once per pollInterval, until shutdown.
func poll(ctx context.Context, q *queue.Queue, queues []string) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now := time.Now()
			for _, name := range queues {
				moved, err := q.PromoteDue(ctx, name, now, promoteBatch)
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					log.Printf("promote due (queue %s): %v", name, err)
					continue
				}
				if moved > 0 {
					log.Printf("promoted %d due job(s) onto queue %s", moved, name)
				}
			}
		}
	}
}

// process loads the job, marks it processing, dispatches it to its handler via
// the registry, and records the outcome -- success, a scheduled retry, or
// dead-lettering.
func process(ctx context.Context, st *store.Store, q *queue.Queue, registry *jobs.Registry, jobID string) error {
	id, err := uuid.Parse(jobID)
	if err != nil {
		return err // a malformed id maps to no row; nothing to do but drop it
	}

	// Claim the job: flip pending -> processing atomically and load it in one shot.
	// A non-pending job (cancelled via the API, or already taken) yields
	// ErrJobNotClaimable and is skipped -- this is what makes POST /cancel stick.
	job, err := st.ClaimJob(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrJobNotClaimable) {
			log.Printf("job %s skipped (cancelled or already claimed)", id)
			return nil
		}
		return err
	}

	// Dispatch by job_type through the registry. The worker knows nothing about
	// specific types -- the registry looks up the handler, runs it, and returns
	// the encoded result (or an error for an unknown type / handler failure).
	result, handlerErr := registry.Dispatch(ctx, job.JobType, job.Payload)
	if handlerErr != nil {
		return handleFailure(ctx, st, q, job, handlerErr)
	}

	log.Printf("job %s completed (type=%s)", id, job.JobType)
	return st.MarkCompleted(ctx, id, result)
}

// handleFailure applies the retry policy when a handler errors:
//
//   - Bump the attempt count. While it stays below max_retries, schedule another
//     run after an exponential backoff of 2^attempts seconds (2s, 4s, 8s, ...).
//   - Once the attempt count reaches max_retries, give up: mark the job failed
//     and record it in the dead-letter queue.
//
// State is persisted to Postgres BEFORE the delayed re-enqueue, matching the
// project's "persist before enqueue" rule: a crash in between leaves a
// recoverable row, never a queued id pointing at stale state.
func handleFailure(ctx context.Context, st *store.Store, q *queue.Queue, job *models.Job, handlerErr error) error {
	attempts := job.RetryCount + 1 // this attempt just failed

	// Out of retries: dead-letter and stop.
	if attempts >= job.MaxRetries {
		log.Printf("job %s dead-lettered after %d attempt(s): %v", job.ID, attempts, handlerErr)
		return st.DeadLetter(ctx, job.ID, attempts, handlerErr.Error())
	}

	// Otherwise schedule a retry after an exponential backoff.
	backoff := backoffFor(attempts)
	if err := st.MarkForRetry(ctx, job.ID, attempts, handlerErr.Error()); err != nil {
		return err
	}
	if err := q.EnqueueDelayed(ctx, job.QueueName, job.ID.String(), time.Now().Add(backoff)); err != nil {
		return err
	}
	log.Printf("job %s failed (attempt %d/%d), retrying in %s: %v",
		job.ID, attempts, job.MaxRetries, backoff, handlerErr)
	return nil
}

// backoffFor returns the delay before the given attempt is retried: 2^attempt
// seconds, clamped to maxBackoff.
func backoffFor(attempt int) time.Duration {
	if attempt >= 30 { // 2^30 s already dwarfs maxBackoff; avoid an oversized shift
		return maxBackoff
	}
	if d := time.Duration(1<<attempt) * time.Second; d < maxBackoff {
		return d
	}
	return maxBackoff
}

// heartbeat registers this worker's liveness in Redis on a fixed interval so the
// admin stats endpoint can report an "active workers" count. We write an initial
// beat immediately (so the worker appears without waiting a full interval) and
// deregister on shutdown so a cleanly stopped worker drops out of the count at
// once rather than ageing out.
func heartbeat(ctx context.Context, q *queue.Queue, workerID string) {
	beat := func() {
		if err := q.Heartbeat(ctx, workerID); err != nil && ctx.Err() == nil {
			log.Printf("heartbeat: %v", err)
		}
	}
	beat()

	ticker := time.NewTicker(queue.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			// ctx is already cancelled, so use a fresh short-lived context to
			// deregister before we exit.
			rmCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			if err := q.RemoveWorker(rmCtx, workerID); err != nil {
				log.Printf("deregister worker: %v", err)
			}
			cancel()
			return
		case <-ticker.C:
			beat()
		}
	}
}
