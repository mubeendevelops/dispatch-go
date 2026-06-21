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
