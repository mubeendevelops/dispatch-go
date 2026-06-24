// Package queue is a thin Redis-backed work queue. Postgres holds the durable
// job state; Redis only carries job IDs as a fast, blocking work signal.
//
// A queue is a Redis list. Producers LPUSH ids onto the left; workers BRPOP from
// the right. Push-left + pop-right gives FIFO ordering.
package queue

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// keyPrefix namespaces our list keys so they never collide with other data in a
// shared Redis instance. Queue "default" lives at "dispatch:queue:default".
const keyPrefix = "dispatch:queue:"

// delayedKeyPrefix namespaces the per-queue delayed sets. The delayed jobs for
// queue "default" live in the sorted set "dispatch:delayed:default".
const delayedKeyPrefix = "dispatch:delayed:"

// Queue wraps a go-redis client.
type Queue struct {
	rdb *redis.Client
}

// New builds a Queue. It does not dial yet -- go-redis connects lazily; call Ping
// to verify connectivity at startup.
func New(addr, password string, db int) *Queue {
	return &Queue{rdb: redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})}
}

// Ping reports whether Redis is reachable (used by the API health check).
func (q *Queue) Ping(ctx context.Context) error { return q.rdb.Ping(ctx).Err() }

// Close closes the underlying client.
func (q *Queue) Close() error { return q.rdb.Close() }

// Enqueue pushes a job id onto the left of the queue's list.
func (q *Queue) Enqueue(ctx context.Context, queueName, jobID string) error {
	if err := q.rdb.LPush(ctx, keyPrefix+queueName, jobID).Err(); err != nil {
		return fmt.Errorf("lpush %q: %w", queueName, err)
	}
	return nil
}

// Dequeue blocks until a job id is available on any of the given queues, or until
// timeout elapses. We use BRPOP (blocking) rather than polling RPOP so an idle
// worker burns no CPU and picks up work the instant it arrives. The finite
// timeout (instead of blocking forever) lets the caller check for shutdown
// between attempts.
//
// On timeout BRPOP returns redis.Nil, which callers treat as "no work, retry".
// When several queues are watched, the returned queueName says which one won.
func (q *Queue) Dequeue(ctx context.Context, timeout time.Duration, queueNames ...string) (jobID, queueName string, err error) {
	keys := make([]string, len(queueNames))
	for i, name := range queueNames {
		keys[i] = keyPrefix + name
	}

	// BRPOP returns a two-element slice: [key, value].
	res, err := q.rdb.BRPop(ctx, timeout, keys...).Result()
	if err != nil {
		return "", "", err // includes redis.Nil on timeout
	}
	return res[1], strings.TrimPrefix(res[0], keyPrefix), nil
}

// Len returns the number of pending ids in a queue (handy for stats and tests).
func (q *Queue) Len(ctx context.Context, queueName string) (int64, error) {
	return q.rdb.LLen(ctx, keyPrefix+queueName).Result()
}

// --- Delayed queue (retry backoff) -----------------------------------------
//
// A retry isn't pushed straight back onto the work list; that would re-run it
// immediately and hammer a failing dependency. Instead we park the job id in a
// per-queue sorted set ("dispatch:delayed:<q>") scored by the Unix time it should
// next run. The set is therefore always ordered by due time, so finding "what's
// ready now" is a cheap range query from the front. A poller (PromoteDue) moves
// due ids back onto the work list. Postgres still holds the durable job state;
// this set only schedules *when* an id re-enters the queue.

// EnqueueDelayed schedules a job id to become available at runAt.
func (q *Queue) EnqueueDelayed(ctx context.Context, queueName, jobID string, runAt time.Time) error {
	z := redis.Z{Score: float64(runAt.Unix()), Member: jobID}
	if err := q.rdb.ZAdd(ctx, delayedKeyPrefix+queueName, z).Err(); err != nil {
		return fmt.Errorf("zadd %q: %w", queueName, err)
	}
	return nil
}

// promoteScript atomically moves every due id (score <= now) from the delayed
// sorted set (KEYS[1]) onto the work list (KEYS[2]), up to ARGV[2] ids, and
// returns how many it moved.
//
// Atomicity is the whole point of using a Lua script here. Redis runs a script to
// completion with no other commands interleaved, so the read + LPUSH + ZREM for
// each id happen as one indivisible step. That guarantees a job can never land on
// both structures (a duplicate) or vanish from both (a loss) due to a crash or a
// race between commands -- and it means PromoteDue is safe to run from several
// workers at once: each due id is claimed (ZREM'd) by exactly one of them. This
// is our defense against double-promotion.
var promoteScript = redis.NewScript(`
local due = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', ARGV[1], 'LIMIT', 0, ARGV[2])
for _, member in ipairs(due) do
    redis.call('LPUSH', KEYS[2], member)
    redis.call('ZREM', KEYS[1], member)
end
return #due
`)

// PromoteDue moves up to limit jobs whose scheduled time has arrived (score <= now)
// from queueName's delayed set back onto its work list, returning the count moved.
func (q *Queue) PromoteDue(ctx context.Context, queueName string, now time.Time, limit int) (int64, error) {
	keys := []string{delayedKeyPrefix + queueName, keyPrefix + queueName}
	n, err := promoteScript.Run(ctx, q.rdb, keys, now.Unix(), limit).Int64()
	if err != nil {
		return 0, fmt.Errorf("promote due %q: %w", queueName, err)
	}
	return n, nil
}

// DelayedLen returns how many jobs are waiting in queueName's delayed set
// (handy for stats and tests).
func (q *Queue) DelayedLen(ctx context.Context, queueName string) (int64, error) {
	return q.rdb.ZCard(ctx, delayedKeyPrefix+queueName).Result()
}

// --- Job cancellation cleanup ------------------------------------------------

// Remove best-effort deletes a job id from a queue's structures: the work list
// (LREM) and the delayed set (ZREM). Cancellation calls this so a cancelled id
// doesn't linger and skew queue-depth stats. It is best-effort -- the real guard
// against running a cancelled job is the worker's atomic claim (store.ClaimJob).
func (q *Queue) Remove(ctx context.Context, queueName, jobID string) error {
	if err := q.rdb.LRem(ctx, keyPrefix+queueName, 0, jobID).Err(); err != nil {
		return fmt.Errorf("lrem %q: %w", queueName, err)
	}
	if err := q.rdb.ZRem(ctx, delayedKeyPrefix+queueName, jobID).Err(); err != nil {
		return fmt.Errorf("zrem %q: %w", queueName, err)
	}
	return nil
}

// --- Worker liveness (heartbeats) --------------------------------------------
//
// Workers don't hold a connection we can count, so each one periodically writes a
// heartbeat key -- "dispatch:worker:<id>" -- with a short TTL. While the worker
// runs it refreshes the key (resetting the TTL) every HeartbeatInterval; if it
// crashes, the key simply EXPIRES after WorkerTTL and Redis removes it for us, so
// there's no scavenger process or manual pruning to write. "Active workers" is
// then just the number of these keys that currently exist.
//
// (This replaces an earlier sorted-set-of-last-seen-times scheme. Letting Redis
// key expiry do the cleanup is simpler and removes the "prune stale members"
// step -- the trade-off is we no longer keep each worker's last-seen timestamp,
// which we weren't using for anything.)

const workerKeyPrefix = "dispatch:worker:"

// HeartbeatInterval is how often a worker refreshes its heartbeat key. WorkerTTL
// is the key's lifetime -- how long after its last beat a worker still counts as
// active. WorkerTTL is comfortably larger than HeartbeatInterval so one missed
// beat (a GC pause, a brief Redis blip) doesn't expire a healthy worker.
const (
	HeartbeatInterval = 10 * time.Second
	WorkerTTL         = 30 * time.Second
)

// Heartbeat records that workerID is alive: it (re)sets the worker's key with a
// fresh WorkerTTL. Refreshing on each beat is what keeps a live worker counted;
// once the worker stops beating the key lapses within WorkerTTL.
func (q *Queue) Heartbeat(ctx context.Context, workerID string) error {
	if err := q.rdb.Set(ctx, workerKeyPrefix+workerID, time.Now().Unix(), WorkerTTL).Err(); err != nil {
		return fmt.Errorf("worker heartbeat: %w", err)
	}
	return nil
}

// RemoveWorker deletes a worker's heartbeat key immediately, so a cleanly
// stopped worker drops out of the count at once instead of waiting out its TTL.
func (q *Queue) RemoveWorker(ctx context.Context, workerID string) error {
	if err := q.rdb.Del(ctx, workerKeyPrefix+workerID).Err(); err != nil {
		return fmt.Errorf("remove worker: %w", err)
	}
	return nil
}

// CountActiveWorkers returns how many worker heartbeat keys currently exist
// (expired ones are already gone, so the count is self-cleaning).
//
// We SCAN for the keys rather than use KEYS: KEYS walks the whole keyspace in one
// blocking pass -- fine in a test, a latency spike on a shared production Redis --
// whereas SCAN returns them incrementally via a cursor without blocking the
// server. SCAN may return the same key more than once across iterations (e.g.
// during a rehash), so we collect into a set and count distinct keys rather than
// summing batch sizes.
func (q *Queue) CountActiveWorkers(ctx context.Context) (int64, error) {
	seen := make(map[string]struct{})
	var cursor uint64
	for {
		keys, next, err := q.rdb.Scan(ctx, cursor, workerKeyPrefix+"*", 100).Result()
		if err != nil {
			return 0, fmt.Errorf("count active workers: %w", err)
		}
		for _, k := range keys {
			seen[k] = struct{}{}
		}
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return int64(len(seen)), nil
}
