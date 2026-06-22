# DispatchGo

A distributed job queue and scheduler built in Go, with a Next.js dashboard.

This is the **foundation skeleton**: a minimal end-to-end slice you can run and
verify. Enqueue a job over HTTP → it's persisted in Postgres and pushed to Redis
→ a worker pops it, runs a handler, and stores the result.

## Architecture (so far)

```
client ──HTTP──▶  api  ──┬─▶ Postgres (jobs table = source of truth)
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

## API

| Method | Path                        | Description                                  |
| ------ | --------------------------- | -------------------------------------------- |
| GET    | `/healthz`                  | Liveness + Postgres/Redis checks             |
| POST   | `/api/v1/jobs/enqueue`      | Persist + enqueue a job; returns it (202)    |
| GET    | `/api/v1/jobs/{id}`         | Fetch a job by id                            |

All error responses share one shape: `{"error": "message"}`.

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

## Project layout

```
cmd/
  api/         HTTP API; also runs migrations via `go run ./cmd/api migrate`
  worker/      Redis consumer: runs handlers, records results, retries/dead-letters
               failures, and polls the delayed set for due retries
  scheduler/   placeholder for delayed/retry scheduling (later step)
internal/
  config/      env-var configuration loader
  models/      domain types (Job, Status)
  store/       Postgres persistence + migration runner
  queue/       Redis-backed work queue (LPUSH / BRPOP + delayed set)
  handlers/    HTTP handlers, routing, JSON responses
  jobs/        job_type -> handler registry and the built-in job handlers
migrations/    SQL migrations (embedded into the binary)
frontend/      Next.js dashboard (later step)
docker-compose.yml
```

## Roadmap

See the status checklist in `CLAUDE.md`. Next up: the scheduler, full stats, and
the dashboard.
