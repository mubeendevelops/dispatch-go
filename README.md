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

## Stack

- Backend: Go (stdlib + go-chi/chi, redis/go-redis/v9, jackc/pgx/v5, google/uuid)
- Queue: Redis (list per queue; a sorted set for delayed retries comes later)
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
the handler output. The current handler is a hardcoded **echo** that returns the
payload unchanged.

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

## Project layout

```
cmd/
  api/         HTTP API; also runs migrations via `go run ./cmd/api migrate`
  worker/      Redis consumer that runs handlers and records results
  scheduler/   placeholder for delayed/retry scheduling (later step)
internal/
  config/      env-var configuration loader
  models/      domain types (Job, Status)
  store/       Postgres persistence + migration runner
  queue/       Redis-backed work queue (LPUSH / BRPOP)
  handlers/    HTTP handlers, routing, JSON responses
migrations/    SQL migrations (embedded into the binary)
frontend/      Next.js dashboard (later step)
docker-compose.yml
```

## Roadmap

See the status checklist in `CLAUDE.md`. Next up: retries + dead-letter queue,
a handler registry, the scheduler, full stats, and the dashboard.
