# DispatchGo

A distributed job queue and scheduler built in Go, with a Next.js dashboard.

This is the **foundation skeleton**: a minimal end-to-end slice you can run and
verify. Enqueue a job over HTTP → it's persisted in Postgres and pushed to Redis
→ a worker pops it, runs a handler, and stores the result.

## Architecture (so far)

```
client ──HTTP──▶ api ──────┐
scheduler ──cron (1/min)──▶┤  both producers persist to Postgres, THEN push the id to Redis
                           ├─▶ Postgres (jobs = source of truth; job_schedules = recurring defs)
                           └─▶ Redis    (list per queue = work signal, holds job IDs)
                                          │
                                  worker  ◀┘  BRPOP id → load job → run handler → save result
```

- **Postgres is the source of truth.** A job is persisted *before* its id is
  pushed to Redis, so a crash between the two can only leave a recoverable row,
  never a queued id pointing at nothing.
- **Redis carries only job IDs.** Producers `LPUSH`, workers `BRPOP` (blocking,
  so idle workers use no CPU and pick up work instantly). Push-left + pop-right = FIFO.
- **Failures retry with backoff, then dead-letter.** A failed job is parked in a
  per-queue Redis *sorted set* scored by its next run time; a poller promotes it
  back onto the work list once due. After it exhausts its retries it's marked
  `failed` and recorded in the `dead_letter_queue` table. See
  [Retries & the dead-letter queue](#retries--the-dead-letter-queue).
- **Recurring jobs come from the scheduler.** `cmd/scheduler` reads cron-based
  rows from `job_schedules` and produces jobs through the *same* persist-before-
  enqueue path as the API. It advances each schedule's `next_run_at` cursor in
  Postgres *before* enqueuing — that ordering is what stops it double-firing
  across restarts. See [Recurring schedules](#recurring-schedules-the-scheduler).

## Stack

- Backend: Go (stdlib + go-chi/chi, redis/go-redis/v9, jackc/pgx/v5, google/uuid)
- Queue: Redis (a list per queue for work, plus a sorted set per queue for delayed retries)
- Persistence: PostgreSQL
- Frontend: Next.js 14 (added in a later step)
- Local infra: Docker Compose

## Prerequisites

- Go 1.25+
- Docker (for Postgres + Redis)

## Setup

### 1. Start infrastructure (Postgres + Redis)

```bash
docker compose up -d
```

> **Note:** the Compose v2 plugin must be installed (`docker compose version`).
> If it isn't available on your machine, start the same two services directly:
>
> ```bash
> docker run -d --name dispatch-postgres \
>   -e POSTGRES_USER=dispatch -e POSTGRES_PASSWORD=dispatch -e POSTGRES_DB=dispatch \
>   -p 5433:5432 postgres:16-alpine
> docker run -d --name dispatch-redis -p 6379:6379 redis:7-alpine
> ```

Postgres is published on host port **5433** (not 5432) to avoid clashing with any
Postgres already running locally. Redis is on 6379.

### 2. Configuration

The services read config from environment variables only. The built-in defaults
already match `docker-compose.yml`, so **for local dev you don't need a `.env`
file**. To override, copy the example and export the values:

```bash
cp .env.example .env
set -a; source .env; set +a   # export everything in .env into your shell
```

### 3. Run database migrations

```bash
go run ./cmd/api migrate
```

### 4. Start the API

```bash
go run ./cmd/api
# api listening on :8080
```

### 5. Start a worker (in another terminal)

```bash
go run ./cmd/worker
# worker watching queues [default]
```

### 6. Start the scheduler (in another terminal)

```bash
go run ./cmd/scheduler
# scheduler started; checking schedules every 1m0s
```

The scheduler fires recurring jobs from the `job_schedules` table. The migrations
seed one example — an `echo` job every minute — so within a minute you'll see it
log a fire and a new job appear (the worker from step 5 then runs it). See
[Recurring schedules](#recurring-schedules-the-scheduler).

### Running all three services

`api`, `worker`, and `scheduler` are independent processes that share Postgres +
Redis. Start infra and apply migrations once, then run each in its own terminal:

```bash
docker compose up -d        # Postgres + Redis (or the docker run fallback above)
go run ./cmd/api migrate    # create tables + seed the example schedule

go run ./cmd/api            # terminal 1 — HTTP API on :8080
go run ./cmd/worker         # terminal 2 — consumes the queue and runs jobs
go run ./cmd/scheduler      # terminal 3 — fires recurring schedules
```

The worker is what actually executes jobs, so keep one running alongside the
scheduler to watch scheduled echoes complete.

## API

| Method | Path                         | Description                                          |
| ------ | ---------------------------- | ---------------------------------------------------- |
| GET    | `/healthz`                   | Liveness + Postgres/Redis checks                     |
| GET    | `/api/v1/jobs`               | List jobs (filter by queue/status, paginated)        |
| POST   | `/api/v1/jobs/enqueue`       | Persist + enqueue a job; returns it (202)            |
| GET    | `/api/v1/jobs/{id}`          | Fetch a job by id                                    |
| POST   | `/api/v1/jobs/{id}/retry`    | Reset a failed job to pending and re-enqueue it      |
| POST   | `/api/v1/jobs/{id}/cancel`   | Cancel a pending job                                 |
| GET    | `/api/v1/admin/stats`        | Queue depths, throughput, latency, failures, workers |
| GET    | `/api/v1/admin/dashboard`    | Totals by status, jobs today, recent jobs            |

Every response is JSON. Errors all share one shape — `{"error": "message"}` — with
the matching status code: `400` validation, `404` not found, `409` conflict (wrong
state), `5xx` server. The API also sends CORS headers for the dashboard origin
(`CORS_ALLOWED_ORIGIN`, default `http://localhost:3000`) and answers preflight
`OPTIONS` requests.

### Enqueue a job

```bash
curl -X POST http://localhost:8080/api/v1/jobs/enqueue \
  -H "Content-Type: application/json" \
  -d '{
    "queue_name": "default",
    "job_type": "echo",
    "payload": {"hello": "world"},
    "max_retries": 3
  }'
```

Returns `202 Accepted` with the created job (`status: "pending"`). Only `job_type`
is required; `queue_name` defaults to `default`, `payload` to `{}`, `max_retries` to 3.

### Check a job's status

```bash
curl http://localhost:8080/api/v1/jobs/<job_id>
```

Once a worker has processed it, `status` becomes `completed` and `result` holds
the handler output. Which handler runs is chosen by `job_type` via a registry —
see [Job types](#job-types).

### List jobs

```bash
curl "http://localhost:8080/api/v1/jobs?status=completed&queue=default&limit=20&offset=0"
```

All query params are optional: `queue`, `status` (one of `pending`, `processing`,
`completed`, `failed`, `cancelled`), `limit` (1–100, default 20), `offset`
(default 0). The response is a page of jobs (newest first) plus the total match
count for pagination:

```json
{
  "jobs": [ { "id": "…", "status": "completed", "...": "..." } ],
  "total": 42,
  "limit": 20,
  "offset": 0
}
```

Invalid params return `400` — e.g. an unknown `status`, or `limit` out of range.

### Retry a failed job

```bash
curl -X POST http://localhost:8080/api/v1/jobs/<job_id>/retry
```

Resets a **failed** job back to `pending` (clears `retry_count`, the error, and
the result) and deletes its dead-letter row in one transaction, then re-enqueues
it (persist-before-enqueue). Returns the reset job (`200`). Only failed jobs are
retryable: a pending/processing/completed job returns `409`; an unknown id `404`.

### Cancel a pending job

```bash
curl -X POST http://localhost:8080/api/v1/jobs/<job_id>/cancel
```

Marks a **pending** job `cancelled` so it never runs, and drops its id from Redis.
Only pending jobs can be cancelled — one already `processing` is in flight and is
not preempted (`409`). Returns the cancelled job (`200`).

> **Why cancellation is race-proof.** Flipping the row to `cancelled` isn't enough
> on its own — a worker may already have popped the id. So the worker *claims* each
> job with an atomic `UPDATE … WHERE status = 'pending'` (`store.ClaimJob`); a job
> that was cancelled (or already taken) updates no row and is skipped. Removing the
> id from Redis is just best-effort cleanup so queue depth stays accurate.

### Admin: stats

```bash
curl http://localhost:8080/api/v1/admin/stats
```

```json
{
  "queues": [ { "queue": "default", "depth": 0, "delayed": 0 } ],
  "processing_rate_per_min": 3,
  "avg_latency_ms": 1.53,
  "failure_rate": 0.25,
  "active_workers": 1
}
```

- **queues** — per-queue backlog: `depth` is the Redis work-list length, `delayed`
  the retry-backoff set length. Covers configured queues plus any seen in the DB.
- **processing_rate_per_min** — jobs completed in the last minute.
- **avg_latency_ms** — average `completed_at − started_at` over the last hour.
- **failure_rate** — `failed / (failed + completed)` over the last hour (0–1).
- **active_workers** — workers that heartbeated within 30s. Each worker writes a
  heartbeat to a Redis sorted set every 10s and deregisters on clean shutdown; a
  crashed worker simply ages out of the count.

### Admin: dashboard

```bash
curl http://localhost:8080/api/v1/admin/dashboard
```

```json
{
  "totals_by_status": { "pending": 0, "processing": 0, "completed": 3, "failed": 1, "cancelled": 1 },
  "jobs_today": 5,
  "recent_jobs": [ { "id": "…", "...": "..." } ]
}
```

`totals_by_status` always carries every status key (zero if none) so the frontend
can rely on the shape, `jobs_today` counts jobs created since the start of the
server's day, and `recent_jobs` is the 10 newest jobs.

## Job types

`job_type` selects the handler that runs a job. Handlers live in `internal/jobs`
and are registered in one place — `jobs.DefaultRegistry()`. An unrecognized
`job_type` fails with a clear error (`no handler registered for job_type "..."`)
and then follows the normal retry/dead-letter path.

| `job_type`    | Payload fields                     | Result on success            | Notes                                             |
| ------------- | ---------------------------------- | ---------------------------- | ------------------------------------------------- |
| `send_email`  | `to` (required), `subject`, `body` | `{message_id, to, status}`   | Simulated send; returns a fake message id.        |
| `export_pdf`  | `document_id` (optional)           | `{document_id, url, status}` | Simulated slow job: sleeps ~3s, returns fake URL. |
| `echo`        | any JSON object                    | the payload, unchanged       | Smoke-test handler.                               |
| `always_fail` | any JSON object                    | —                            | Always errors; exercises retries + the DLQ.       |

```bash
# send_email
curl -s -X POST http://localhost:8080/api/v1/jobs/enqueue \
  -H "Content-Type: application/json" \
  -d '{"job_type":"send_email","payload":{"to":"user@example.com","subject":"Hi"}}'

# export_pdf (completes after ~3s)
curl -s -X POST http://localhost:8080/api/v1/jobs/enqueue \
  -H "Content-Type: application/json" \
  -d '{"job_type":"export_pdf","payload":{"document_id":"doc_123"}}'
```

**Adding a job type:** implement the `jobs.JobHandler` interface in
`internal/jobs` and add one `Register(...)` line to `jobs.DefaultRegistry()`.

## How to verify

1. `docker compose up -d` (or the `docker run` fallback above) — Postgres + Redis start.
2. `go run ./cmd/api migrate` — prints `migrations applied`.
3. `go run ./cmd/api` — prints `api listening on :8080`.
4. `go run ./cmd/worker` (separate terminal) — prints `worker watching queues [default]`.
5. `curl http://localhost:8080/healthz` — returns `{"status":"ok","postgres":"ok","redis":"ok"}`.
6. POST the enqueue example above — returns `202` and a JSON job with an `id` and `status: "pending"`.
7. `GET /api/v1/jobs/<id>` — within a moment `status` is `completed` and `result` equals the payload.
8. The worker terminal logs `job <id> completed (type=echo)`.

Error checks: an unknown job id returns `404`, a malformed id returns `400`, and
enqueue without `job_type` returns `400`.

## Retries & the dead-letter queue

### How a failure flows

When a handler returns an error, the worker:

1. **Bumps `retry_count`.** While it stays below `max_retries`, the job is
   scheduled for another attempt; at the limit it's dead-lettered (below).
2. **Schedules a retry with exponential backoff** of `2^retry_count` seconds —
   2s, 4s, 8s, … (capped at 1h). The job is set back to `pending` in Postgres
   first, then its id is added to the delayed queue (persist-before-enqueue).
3. **Dead-letters at the limit.** Once `retry_count` reaches `max_retries`, the
   job is marked `failed` and a row is written to the `dead_letter_queue` table
   (`job_id`, `reason`, `attempts`, `created_at`) — both in one transaction so
   they can't diverge.

With the default `max_retries` of 3, an always-failing job is attempted 3 times
(waiting 2s then 4s) before landing in the DLQ.

### The delayed-queue design

A retry can't go straight back onto the work list — that would re-run it
instantly and hammer whatever just failed. Instead:

- Each queue has a companion **sorted set** `dispatch:delayed:<queue>`. The
  member is the job id and the **score is the Unix time it should next run**. A
  sorted set stays ordered by due time, so "what's ready now?" is a cheap range
  query from the front (`ZRANGEBYSCORE … -inf <now>`).
- A **poller** inside the worker wakes once a second and moves every due id from
  the delayed set onto the work list, where a normal `BRPOP` picks it up.
- The move runs as a small **Lua script** so the read, the `LPUSH`, and the
  `ZREM` execute **atomically** server-side. That's what stops a job from ending
  up on both structures (a duplicate) or neither (a loss), and it makes the
  poller safe to run from many workers at once — each due id is claimed by
  exactly one. This is our guard against double-promotion.

Postgres stays the source of truth throughout; the sorted set only schedules
*when* an id re-enters the queue.

### Testing it

With infra up and the API + worker running (see [How to verify](#how-to-verify)):

```bash
# Enqueue a job that always fails (max_retries defaults to 3).
ID=$(curl -s -X POST http://localhost:8080/api/v1/jobs/enqueue \
  -H "Content-Type: application/json" \
  -d '{"job_type":"always_fail","payload":{}}' | jq -r .id)

# Watch the worker terminal — it logs the whole sequence:
#   job <id> failed (attempt 1/3), retrying in 2s: ...
#   promoted 1 due job(s) onto queue default
#   job <id> failed (attempt 2/3), retrying in 4s: ...
#   promoted 1 due job(s) onto queue default
#   job <id> dead-lettered after 3 attempt(s): ...

# After ~6s the job is terminal:
curl -s http://localhost:8080/api/v1/jobs/$ID | jq '{status, retry_count, error_message}'
# -> { "status": "failed", "retry_count": 3, "error_message": "always_fail: ..." }

# And it's recorded in the dead-letter queue:
psql "$DATABASE_URL" -c "SELECT job_id, attempts, reason FROM dead_letter_queue;"

# (Optional) peek at the delayed set while a retry is pending — member is the job
# id, score is its due time. Adjust the connection to your Redis:
redis-cli -p 6379 ZRANGE dispatch:delayed:default 0 -1 WITHSCORES
```

> No `psql`/`redis-cli` on your host? Run them inside the containers instead, e.g.
> `docker exec <postgres-container> psql -U dispatch -d dispatch -c '…'` and
> `docker exec <redis-container> redis-cli …`.

## Recurring schedules (the scheduler)

`cmd/scheduler` turns recurring definitions into real jobs. A row in the
`job_schedules` table is a **cron expression** plus a **job template**
(`job_type` + `payload`):

| Column                 | Meaning                                                   |
| ---------------------- | --------------------------------------------------------- |
| `cron_expression`      | When to fire — standard 5-field crontab (e.g. `* * * * *`).|
| `job_type` + `payload` | The job to enqueue each time it fires.                    |
| `enabled`              | Turn a schedule off without deleting it.                  |
| `last_run_at`          | When it last fired (`NULL` until the first).              |
| `next_run_at`          | **The durable cursor**: the next time it is due to fire.  |

The loop is deliberately simple: **once a minute** the scheduler asks Postgres for
the enabled schedules whose `next_run_at` has passed, and fires each one. Cron
parsing uses [`robfig/cron`](https://github.com/robfig/cron)'s `ParseStandard`, so
the `@every 30s` / `@daily` shorthands work too. Because the tick is one minute, a
schedule fires up to ~1 minute after its due time — fine for minute-resolution
cron, and the latency knob (`tickInterval`) is one constant.

The seed migration adds one schedule — an `echo` job **every minute** — so you can
watch it fire the moment the scheduler starts.

### Firing one schedule: order is everything

For each due schedule the scheduler does, in this exact order:

1. **Compute** the next run time from the cron expression.
2. **Claim** the run: advance the cursor (`last_run_at`, `next_run_at`) in Postgres
   with a *compare-and-swap* — `UPDATE … WHERE next_run_at = <the value we just
   read>`.
3. **Enqueue** the job — only if the claim won — via the same persist-before-
   enqueue path the API uses.

### Why this prevents double-runs on restart

A schedule's state lives entirely in Postgres (`next_run_at`), never in the
scheduler's memory. A restarted — or replacement — scheduler just re-reads
`next_run_at` and continues; there are no in-memory timers to lose.

The **claim-before-enqueue** ordering is what makes a crash safe. The durable
cursor moves into the future *before* the job is enqueued, so if the process dies
anywhere after step 2, the restarted scheduler sees `next_run_at` already in the
future and **skips** the schedule — no re-fire. The trade-off is the opposite,
gentler failure: a crash in the small window *between* the claim and the enqueue
loses that one run (it is never enqueued). For a recurring job that's the right
call — a missed tick self-heals next minute, whereas a double-fire could send a
duplicate email or double-charge. The design is **at-most-once**, chosen over
at-least-once on purpose.

(Runs missed during downtime are not replayed once per missed minute, either:
`Next(now)` is computed from the current time, so a catch-up collapses to a single
fire — standard cron behaviour, no backfill storm.)

### What changes for multiple instances

You can run several schedulers for high availability, and the **same
compare-and-swap is what makes that safe** — no leader election required. When two
schedulers both see the same due row, both run step 2 with the same expected
`next_run_at`. Postgres serializes the two `UPDATE`s, so exactly one matches a row
(and goes on to enqueue) while the other matches zero rows (and skips). You get one
job per fire, not one per scheduler.

What you'd add for a larger fleet is polish, not correctness:

- a small **random jitter** on the tick so instances don't all wake on the same
  second and pile up identical, mostly-losing `UPDATE`s;
- if you need **at-least-once** instead, do the claim and the job's `INSERT` in one
  Postgres transaction and push to Redis only after it commits, closing the
  "claimed but not enqueued" window.

### Watch it fire

With infra up and migrations applied, run the scheduler (and a worker to execute
what it produces):

```bash
go run ./cmd/scheduler
# scheduler started; checking schedules every 1m0s
# schedule 00000000-…-000000000001 fired: enqueued echo job <id> (next run …)
```

```bash
# the schedule's cursor advanced to the next minute and last_run_at is set:
psql "$DATABASE_URL" -c "SELECT job_type, last_run_at, next_run_at FROM job_schedules;"

# a fresh echo job is produced each minute:
psql "$DATABASE_URL" -c "SELECT id, job_type, status FROM jobs ORDER BY created_at DESC LIMIT 5;"
```

**Restart-safety in one move:** stop the scheduler and start it again within the
same minute — it will *not* re-fire the schedule, because `next_run_at` is already
in the future. (No `psql` on the host? Use `docker exec <postgres-container> psql
-U dispatch -d dispatch -c '…'`.)

## Project layout

```
cmd/
  api/         HTTP API; also runs migrations via `go run ./cmd/api migrate`
  worker/      Redis consumer: runs handlers, records results, retries/dead-letters
               failures, and polls the delayed set for due retries
  scheduler/   wakes every minute; fires due recurring schedules from job_schedules
internal/
  config/      env-var configuration loader
  models/      domain types (Job, Status, JobSchedule)
  store/       Postgres persistence + migration runner (jobs, DLQ, schedules)
  queue/       Redis-backed work queue (LPUSH / BRPOP + delayed set)
  enqueue/     shared persist-before-enqueue path used by the API and scheduler
  handlers/    HTTP API: routing, jobs + admin/stats endpoints, validation, CORS
  jobs/        job_type -> handler registry and the built-in job handlers
migrations/    SQL migrations (embedded into the binary)
frontend/      Next.js dashboard (later step)
docker-compose.yml
```

## Roadmap

See the status checklist in `CLAUDE.md`. Next up: the frontend dashboard.
