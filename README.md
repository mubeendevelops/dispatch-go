# DispatchGo

A distributed job queue and scheduler built in Go.

## Stack

- Go + Chi
- PostgreSQL
- Redis
- Next.js dashboard

## Setup

1. Start infrastructure

```bash
docker compose up
```

2. Run migrations

```bash
go run cmd/api/main.go migrate
```

3. Start API

```bash
go run cmd/api/main.go
```

4. Start worker

```bash
go run cmd/worker/main.go
```

5. Start dashboard

```bash
cd frontend
npm install
npm run dev
```

## Test

Enqueue a job:

```bash
curl -X POST http://localhost:8080/api/v1/jobs/enqueue \
  -H "Content-Type: application/json" \
  -d '{
    "queue_name": "default",
    "job_type": "send_email",
    "payload": {"to": "user@example.com"},
    "max_retries": 3
  }'
```

Check status:

```bash
curl http://localhost:8080/api/v1/jobs/{job_id}
```

Dashboard: http://localhost:3000
