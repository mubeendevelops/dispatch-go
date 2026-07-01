// Command scheduler turns recurring schedules into jobs. Once a minute it loads
// the enabled schedules whose next_run_at has passed, enqueues a job from each
// one's template (via the same persist-before-enqueue path the API uses), and
// advances the schedule's cursor to its next cron time.
//
// The two questions worth answering up front -- how it avoids double-firing
// across restarts, and what changes if you run several schedulers at once -- are
// answered at fireSchedule below, where the ordering that guarantees it lives.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/mubeendevelops/dispatch-go/internal/config"
	"github.com/mubeendevelops/dispatch-go/internal/enqueue"
	"github.com/mubeendevelops/dispatch-go/internal/models"
	"github.com/mubeendevelops/dispatch-go/internal/queue"
	"github.com/mubeendevelops/dispatch-go/internal/store"
)

// tickInterval is how often the scheduler looks for due schedules. Cron
// resolution is one minute, so checking once a minute is enough. The trade-off is
// latency: a schedule fires up to one tick (~1m) after its due time. A shorter
// tick trims that latency at the cost of more (usually empty) queries.
const tickInterval = 1 * time.Minute

func main() {
	cfg := config.Load()

	// Root context is cancelled on SIGINT/SIGTERM so the tick loop unwinds cleanly.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
		<-stop
		log.Println("shutting down scheduler...")
		cancel()
	}()

	st, err := store.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("scheduler: %v", err)
	}
	defer st.Close()

	q := queue.New(cfg.RedisAddr, cfg.RedisPassword, cfg.RedisDB)
	defer q.Close()
	if err := q.Ping(ctx); err != nil {
		log.Fatalf("scheduler: redis: %v", err)
	}

	// The scheduler is just another job producer, so it uses the same enqueue path
	// as the API -- scheduled jobs are indistinguishable from API-submitted ones.
	enq := enqueue.New(st, q)

	log.Printf("scheduler started; checking schedules every %s", tickInterval)

	// Run one pass immediately so a freshly started scheduler doesn't wait a full
	// tick (and promptly catches up anything that came due while it was down), then
	// once per tick until shutdown.
	handleSchedules(ctx, st, enq)

	ticker := time.NewTicker(tickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Println("scheduler stopped")
			return
		case <-ticker.C:
			handleSchedules(ctx, st, enq)
		}
	}
}

// handleSchedules runs one scheduling pass: find every schedule that is due and
// fire each one. A single bad schedule never aborts the pass -- failures are
// logged per schedule and the loop continues.
func handleSchedules(ctx context.Context, st *store.Store, enq *enqueue.Enqueuer) {
	now := time.Now()
	due, err := st.DueSchedules(ctx, now)
	if err != nil {
		if ctx.Err() == nil { // suppress the noise from a shutdown-cancelled query
			log.Printf("load due schedules: %v", err)
		}
		return
	}
	for i := range due {
		if ctx.Err() != nil {
			return
		}
		fireSchedule(ctx, st, enq, now, due[i])
	}
}

// fireSchedule fires one due schedule. The ordering here is the whole ballgame
// for correctness:
//
//  1. Compute the schedule's NEXT run time from its cron expression.
//  2. CLAIM the run by advancing the schedule's cursor (last_run_at, next_run_at)
//     in Postgres -- but only if next_run_at is still the value we read. This is a
//     compare-and-swap (store.ClaimScheduleRun).
//  3. Only if we won the claim, enqueue the job.
//
// Why claim BEFORE enqueue? It makes the scheduler safe to restart. The durable
// cursor (next_run_at) moves into the future first, so a crash anywhere after
// step 2 cannot cause a re-fire: a restarted scheduler reads the advanced
// next_run_at and skips the schedule. The cost is the opposite, gentler failure
// mode -- a crash between the claim and the enqueue means that one run is missed
// (never enqueued). For a recurring schedule that is the right trade: a missed
// tick self-heals on the next one, whereas a double-fire could send a duplicate
// email or double-charge. In short: at-most-once, not at-least-once.
//
// MULTIPLE INSTANCES: the compare-and-swap in step 2 is also what lets several
// schedulers run at once (for high availability). They all read the same due row
// and all try to claim it with the same expected next_run_at; Postgres serializes
// the UPDATEs so exactly one matches a row and proceeds to enqueue, while the
// others match zero rows and skip. So running N schedulers does not produce N
// copies of each job -- no leader election required. What you would add for a
// bigger fleet is mostly polish: a small random jitter on the tick so instances
// don't all wake at the same instant, and (if you need at-least-once) doing the
// claim and the job's INSERT in one Postgres transaction, enqueuing to Redis only
// after it commits.
func fireSchedule(ctx context.Context, st *store.Store, enq *enqueue.Enqueuer, now time.Time, sc models.JobSchedule) {
	// ParseStandard handles the 5-field crontab syntax ("* * * * *") plus the
	// "@every 30s" / "@daily" shorthands. We parse on each fire rather than caching
	// so an edited cron_expression takes effect on the next tick with no restart.
	schedule, err := cron.ParseStandard(sc.CronExpression)
	if err != nil {
		// A malformed expression can never advance, so it would stay "due" forever
		// and fire every tick. Log and skip; fixing or disabling the row is an
		// operator action. We deliberately do not crash the loop over one bad row.
		log.Printf("schedule %s: invalid cron %q: %v", sc.ID, sc.CronExpression, err)
		return
	}

	// Next(now) returns the first activation strictly after now, so next_run_at is
	// always in the future. This collapses runs missed during downtime into a
	// single catch-up fire (standard cron behaviour -- no backfilling every minute
	// the scheduler was offline).
	nextRunAt := schedule.Next(now)

	won, err := st.ClaimScheduleRun(ctx, sc.ID, sc.NextRunAt, now, nextRunAt)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("schedule %s: claim run: %v", sc.ID, err)
		}
		return
	}
	if !won {
		// Another scheduler claimed this run first, or it was disabled between the
		// read and now. Either way there is nothing for us to do.
		return
	}

	// We own this run: enqueue a job from the schedule's template. The fired job
	// inherits the schedule's owning tenant, so scheduler-produced jobs are scoped
	// exactly like API-produced ones (and pass the enqueuer's tenant guard).
	// Schedules carry no queue_name of their own yet, so jobs land on the default
	// queue with the standard retry budget.
	job := &models.Job{
		TenantID:   sc.TenantID,
		JobType:    sc.JobType,
		Payload:    sc.Payload,
		MaxRetries: enqueue.DefaultMaxRetries,
	}
	if err := enq.Submit(ctx, job); err != nil {
		// The cursor is already advanced, so by design we will NOT retry this run.
		// Log loudly. ErrEnqueueAfterPersist means the job row exists and is
		// recoverable; a plain error means nothing was enqueued this tick.
		log.Printf("schedule %s (job_type=%s): claimed but enqueue failed: %v", sc.ID, sc.JobType, err)
		return
	}
	log.Printf("schedule %s fired: enqueued %s job %s (next run %s)",
		sc.ID, sc.JobType, job.ID, nextRunAt.Format(time.RFC3339))
}
