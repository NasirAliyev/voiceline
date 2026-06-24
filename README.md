# Voiceline

A small, production-quality Go (Gin) backend that turns a **voice note into a structured sales note** and delivers it to an external system. Upload audio → it is transcribed and summarized by an LLM → the structured result is delivered to a destination (Google Sheets, or a log sink for zero-setup runs).

The whole thing **builds, tests, and runs entirely in Docker** — no local Go toolchain required.

```
POST audio ──▶ 202 + job id ──▶ [worker pool] ──▶ Whisper (transcribe)
                                                      │
                                                      ▼
                            destination ◀── GPT-4o-mini (extract structured note)
   GET /:id  ◀── poll status ──▶ { queued | processing | completed | failed, result }
```

Processing is **asynchronous**: the upload returns immediately with a job id, a bounded worker pool runs the pipeline in the background, and the client polls a status endpoint for the result.

---

## Architecture

Hexagonal / ports & adapters, with dependencies pointing **inward** (`transport` and `adapter` depend on `domain`; `app` orchestrates `domain` ports; `domain` depends on nothing). The key property: **swapping the LLM provider or the destination means adding one adapter and changing zero core logic.**

```
cmd/server/main.go            Composition root: config, wiring, graceful shutdown
internal/
  config/                     Typed config from env (+ validation, const defaults)
  domain/                     Entities, value objects, PORT interfaces, errors (pure)
  app/                        Use case: processor (transcribe→analyze→deliver) + worker pool
  adapter/
    openai/                   Transcriber + Analyzer (thin net/http client, retries)
    sheets/                   Google Sheets destination + pure row mapping
    logdest/                  Stdout/log destination (zero-setup default)
    memstore/                 In-memory JobStore (RWMutex)
  transport/httpapi/          Gin router, handlers, middleware, DTOs
```

**Ports** (interfaces in `domain`): `Transcriber`, `Analyzer`, `Destination`, `JobStore`. Each adapter implements one; the app layer and tests depend only on the interfaces, so every unit is testable with hand-written fakes and no network.

> The transport package is named `httpapi` (not `http`) so it never collides with the standard `net/http` import at call sites.

---

## Prerequisites

- **Docker** (with the daemon running) and **Docker Compose**. That's it — Go runs only inside the build image.
- An **OpenAI API key**.

> On Docker 20.10 the Makefile enables BuildKit automatically (`DOCKER_BUILDKIT=1`).

---

## Run it (Docker)

```bash
cp .env.example .env
# edit .env and set OPENAI_API_KEY=sk-...   (keep DESTINATION=log for zero setup)

make up        # builds the image and starts the service on :8080
```

The first `make` command auto-runs `go mod tidy` (in a container) to generate `go.mod`/`go.sum`.

Other targets:

```bash
make test      # vet + race-enabled unit tests, hermetically inside Docker
make build     # build the distroless runtime image
make logs      # follow service logs
make down      # stop the stack
make help      # list targets
```

### Try it end to end

```bash
# 1) Upload an audio file (any sample works; e.g. a short .m4a/.mp3/.wav)
curl -s -F "audio=@sample.m4a" http://localhost:8080/api/v1/voicelines
# -> {"id":"<job-id>","status":"queued","status_url":"/api/v1/voicelines/<job-id>"}

# 2) Poll for the result
curl -s http://localhost:8080/api/v1/voicelines/<job-id>
# -> {"id":"...","status":"completed","result":{"title":"...","summary":"...",
#     "key_points":[...],"action_items":[...],"transcript":"..."}}

# Health probe
curl -s http://localhost:8080/healthz   # -> {"status":"ok"}
```

If `API_KEY` is set in `.env`, add `-H "Authorization: Bearer <token>"` (or `-H "X-API-Key: <token>"`) to the two `/api/v1` calls. `/healthz` stays open.

### Deliver to Google Sheets (optional)

By default `DESTINATION=log` and the service needs no external accounts. To append
each note as a row in a Google Sheet instead, follow these steps.

**1. Create a service account and download its key**

- In the [Google Cloud Console](https://console.cloud.google.com/), select (or create) a project.
- Enable the **Google Sheets API**: *APIs & Services → Library → Google Sheets API → Enable*.
- Create the account: *APIs & Services → Credentials → Create credentials → Service account*.
- Open the new account → *Keys → Add key → Create new key → JSON*. A `.json` file downloads.
- Save it as `credentials.json` in the repo root. (It is git-ignored.) Note the account's
  email — it looks like `name@project.iam.gserviceaccount.com`.

**2. Create the spreadsheet and share it with the service account**

- Create a Google Sheet. Its ID is the long string in the URL:
  `https://docs.google.com/spreadsheets/d/`**`<SPREADSHEET_ID>`**`/edit`.
- Click **Share**, paste the service-account email, and grant **Editor**. Without this the
  append fails with a permission error — the service account is a separate identity, not your
  Google login.
- Optional: name the destination tab. The app appends to a tab called `Voicelines` by default
  (override with `GOOGLE_SHEETS_SHEET_NAME`). The tab is **not** auto-created — make sure it
  exists, or change the name to match an existing tab.

**3. Configure `.env`**

```dotenv
DESTINATION=sheets
GOOGLE_SHEETS_SPREADSHEET_ID=<SPREADSHEET_ID>
GOOGLE_APPLICATION_CREDENTIALS=./credentials.json
# GOOGLE_SHEETS_SHEET_NAME=Voicelines   # optional; defaults to Voicelines
```

That's all the configuration needed. `GOOGLE_APPLICATION_CREDENTIALS` is the host path to your
key — compose mounts it read-only into the container automatically, and the same path works when
running without Docker (`make run`). **No `docker-compose.yml` edits are required.**

**4. Run it**

```bash
make up
```

Each processed recording now appends one row: *Timestamp, Job ID, Source File, Title, Summary,
Key Points, Action Items, Transcript*. Upload an audio file (see the `curl` example above) and
the row appears once the job reaches `completed`.

> **Note:** appends are not retried — a Sheets write is non-idempotent, so a retry after a
> partial failure could duplicate a row. Delivery is at-least-once with possible duplicates;
> idempotency keys would be the production follow-up.

---

## API

| Method | Path | Description |
|---|---|---|
| `POST` | `/api/v1/voicelines` | `multipart/form-data` with field `audio`. Validates type/size, returns `202 {id,status,status_url}`. |
| `GET` | `/api/v1/voicelines/:id` | Job status; includes `result` when `completed`, `error` when `failed`. `404` if unknown. |
| `GET` | `/healthz` | Liveness probe (unauthenticated). |

Status codes: `400` empty/missing/invalid upload · `401` bad/missing token · `413` too large · `415` unsupported type · `503` queue full (with `Retry-After`).

---

## Configuration

Loaded once at startup; fails fast with a clear error if required values are missing. Only `OPENAI_API_KEY` is mandatory.

| Variable | Default | Notes |
|---|---|---|
| `PORT` | `8080` | |
| `SERVER_READ_TIMEOUT` | `15s` | Also slowloris protection. Raise for large uploads on slow links. |
| `SERVER_WRITE_TIMEOUT` | `15s` | |
| `SERVER_SHUTDOWN_TIMEOUT` | `30s` | Bounds in-flight drain on shutdown. |
| `API_KEY` | _(empty)_ | If set, bearer/`X-API-Key` auth is enforced. |
| `OPENAI_API_KEY` | — | **Required.** |
| `OPENAI_BASE_URL` | `https://api.openai.com/v1` | Any OpenAI-compatible endpoint. |
| `OPENAI_TRANSCRIPTION_MODEL` | `whisper-1` | |
| `OPENAI_ANALYSIS_MODEL` | `gpt-4o-mini` | Cheap default; configurable. |
| `OPENAI_REQUEST_TIMEOUT` | `60s` | Per-attempt. |
| `OPENAI_MAX_RETRIES` | `2` | Retries on 429/5xx/network errors. |
| `DESTINATION` | `log` | `log` or `sheets`. |
| `GOOGLE_SHEETS_SPREADSHEET_ID` | — | Required when `DESTINATION=sheets`. |
| `GOOGLE_SHEETS_SHEET_NAME` | `Voicelines` | |
| `GOOGLE_APPLICATION_CREDENTIALS` | — | Service-account JSON path. |
| `PIPELINE_WORKERS` | `4` | Bounded concurrency. |
| `PIPELINE_QUEUE_SIZE` | `32` | Backpressure buffer; full → `503`. |
| `PIPELINE_PROCESSING_TIMEOUT` | `120s` | Per-job deadline. |
| `MAX_AUDIO_BYTES` | `26214400` | 25 MiB (Whisper limit). |
| `ALLOWED_AUDIO_TYPES` | _(see `.env.example`)_ | Comma-separated MIME allowlist. |
| `LOG_LEVEL` / `LOG_FORMAT` | `info` / `json` | |

---

## Design decisions & trade-offs

- **Async ingest + bounded worker pool.** Request latency is decoupled from LLM latency; concurrency and throughput are tunable. A full queue returns `503` (backpressure) instead of growing memory unboundedly.
- **Detached worker context.** The HTTP request context is cancelled the moment `202` is written, so the worker runs the pipeline under a **fresh, timeout-bounded context** (rooted at the pool, not the request). This is the subtle-but-correct handling of "pass context into goroutines."
- **Uploads are spooled to a temp file**, not buffered in memory — memory stays flat regardless of audio size. The transcriber re-opens that file on each attempt, so **retries resend the full audio** rather than a drained stream.
- **Structured Outputs** (strict JSON schema) make extraction schema-valid by construction; the provider payload is mapped into a canonical, provider-agnostic `Analysis`.
- **UUIDv4 job ids.** The status endpoint exposes a transcript (PII), so ids are unguessable to prevent enumeration of other users' results.
- **Security:** optional bearer auth (constant-time compare), upload size + content-type validation (and `http.MaxBytesReader`), secrets only via env, sanitized client-facing errors (raw provider errors are logged server-side, never returned or stored on the job), timeouts on every inbound/outbound call. TLS termination is a deployment concern (reverse proxy / load balancer) and out of scope here.
- **Container:** multi-stage build runs the test suite as a build stage (hermetic), produces a static binary on a **distroless non-root** image with no shell; the health probe reuses the binary's own `healthcheck` subcommand.

## Known limitations

- **In-memory job store is non-persistent and grows unbounded** (completed jobs are never evicted). Fine for a single instance; for horizontal scale, implement `JobStore` over Redis/SQL (same port) and add TTL/eviction.
- **Delivery is at-least-once.** The Sheets append is a non-idempotent mutation and is deliberately not retried, but a partial failure after the row is written could still duplicate it. Idempotency keys would be the production follow-up.
- **25 MiB upload cap** (Whisper limit); chunking longer audio is future work.

## Non-goals (intentionally omitted to stay lean)

No external broker (in-memory queue behind an interface — swapping to SQS/Redis is one adapter), no database, no Kubernetes/Helm, a single reference LLM adapter, single-token auth (no user management), and structured logs + request-id instead of a full observability stack.
