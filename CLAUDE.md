# CLAUDE.md

Operating instructions for Claude Code. Read this fully before writing any code. Build exactly what is specified here, follow the conventions, and respect the Non-Goals. Prefer the simplest design that satisfies the requirements — **do not over-engineer.**

---

## 1. Goal

Build a small, production-quality **Go (Gin) backend** that:

1. Receives **audio** over HTTP.
2. Sends the audio to an **LLM API** that transcribes it and extracts a **structured note**.
3. Delivers that structured note to an **external destination** (a "sink") such as a webhook, Google Sheets, or a CRM.

This is a take-home assignment. It will be judged on architecture, code quality, testing, security, and how quickly a reviewer can spin it up. Optimize for **clarity and good judgment**, not feature count.

The pipeline intentionally mirrors a real product flow (voice → structured data → external system). Model the domain accordingly.

---

## 2. Functional requirements

### Endpoints
- `POST /v1/recordings`
  - Accepts `multipart/form-data` with a file field named `audio`.
  - Validates content type (against a configurable allowlist) and size (against a configurable max). Reject empty bodies.
  - **Does not block** on processing. Enqueues the job and returns `202 Accepted` immediately with `{ "id": "<job-id>", "status": "queued" }`.
- `GET /v1/recordings/:id`
  - Returns current job status and, if completed, the structured note. Status is one of `queued | processing | completed | failed`.
- `GET /healthz`
  - Liveness probe. Returns `200` with minimal body.

### Processing flow (asynchronous, behind the queue)
1. A worker pulls a job from the queue.
2. It calls the **AudioProcessor** (LLM) to transcribe the audio and extract a structured `Note`.
3. It maps the LLM output into the internal **canonical `Note` model**.
4. It calls the configured **NoteSink** to deliver the `Note`.
5. It updates the job's status/result in an in-memory job store. On transient failure it retries with backoff up to a configurable limit; after exhausting retries it marks the job `failed` and logs with context (no crash).

---

## 3. Architecture

Use **layered / hexagonal (ports & adapters) architecture** with dependencies pointing inward. Apply **DDD** where it adds clarity — model a real domain, not anemic structs.

### Directory layout
```
.
├── cmd/server/main.go            # Composition root: load config, wire deps, start server + workers, graceful shutdown
├── internal/
│   ├── config/                   # Typed Config struct, env loading + validation, defaults via consts
│   ├── domain/                   # Entities, value objects, domain errors, PORT interfaces (no framework imports)
│   ├── app/                      # Application/use-case services that orchestrate domain ports
│   ├── transport/http/           # Gin router, handlers, middleware (auth, request-id, recovery), request/response DTOs
│   ├── queue/                    # Queue interface + in-memory implementation + bounded worker pool
│   ├── adapter/llm/              # AudioProcessor implementation (e.g. OpenAI) — the only LLM adapter required
│   ├── adapter/sink/             # NoteSink implementations: webhook (default), stdout (local), googlesheets (optional)
│   └── store/                    # In-memory job store (status/result) behind an interface
├── Dockerfile
├── docker-compose.yml
├── Makefile
├── .env.example
├── .gitignore
├── go.mod
└── README.md
```

### Domain model (canonical, provider-agnostic — this is the centerpiece)
- `Recording` (aggregate root): `JobID`, received timestamp, audio metadata (filename, content type, size), `Status`, optional `*Note` result, optional error detail.
- `Note` (the canonical structured output the whole system speaks): e.g. `Summary`, `Customer` (value object: name/company), `ActionItems` ([]value object: description + optional due date), optional `Tags`, and `Transcript`. Keep it focused on a sales-visit note.
- Value objects: `JobID`, `Customer`, `ActionItem` — small, immutable, validated on construction.
- Domain errors (sentinel, wrapped with `%w` at boundaries): `ErrUnsupportedAudioType`, `ErrAudioTooLarge`, `ErrEmptyAudio`, `ErrProcessingFailed`, `ErrSinkDeliveryFailed`, `ErrJobNotFound`.

### Ports (interfaces, defined in `domain`; implemented in `adapter/*`)
- `AudioProcessor`: `Process(ctx context.Context, in AudioInput) (Note, error)` — transcribe + extract in one capability.
- `NoteSink`: `Deliver(ctx context.Context, note Note) error`.
- `JobStore`: get/set job status and result.
- `Queue` (in `queue` package): submit a job; the worker pool consumes and invokes a handler.

The key property to demonstrate: **swapping the LLM provider or the destination means adding one adapter and changing zero core logic.** Make that obvious in the code and call it out in the README.

---

## 4. Technology choices

- **Go 1.22+** (use modules; pin a current Go version in `go.mod` and the Dockerfile).
- **Gin** for HTTP.
- **`log/slog`** (stdlib) for structured logging. No third-party logging library.
- **Config from environment variables** via a small typed loader using stdlib `os`. `github.com/joho/godotenv` is allowed **for local dev only** (load `.env` if present; never required in production).
- **LLM (default reference adapter): OpenAI** — audio transcription endpoint, then a chat completion requesting **JSON output** for the structured note. The provider base URL, API key, and model names must all be configurable so any OpenAI-compatible endpoint can be used. Implement **one** adapter only; note in code/README that others fit behind the same port.
- **Sink (default): generic HTTP webhook** (`POST` JSON to a configurable URL) plus a **stdout** sink for zero-setup local runs. **Google Sheets** (append-row via service account) is an optional bonus adapter — only add it if it doesn't compromise the "spin up in one command with no external accounts" experience.
- **Testing:** Go `testing` with table-driven tests. `github.com/stretchr/testify` is allowed for assertions. Use hand-written mocks/stubs that satisfy the port interfaces — no mock-generation tooling.

Keep the dependency list minimal. Every dependency should earn its place.

---

## 5. Coding conventions (mandatory)

- **No hard-coded values.** All tunables come from config. Use **`const`** for fixed defaults, header names, status strings, content-type strings, route paths, and limits. No magic numbers or literals scattered in logic.
- **Naming:** clear, idiomatic, readable. No abbreviations that aren't standard Go. Exported identifiers have doc comments.
- **Errors:** return and wrap errors (`fmt.Errorf("...: %w", err)`). Handle at boundaries. **No `panic`, no `log.Fatal`, no `os.Exit` outside `cmd/server/main.go`.** The HTTP layer maps domain errors to status codes (400 for validation, 404 for not found, 502/503 for downstream/queue issues, 500 otherwise).
- **Context:** every exported method that does I/O or may block takes `context.Context` as its first argument. **Every goroutine receives and respects a context** for cancellation. All external calls (LLM, sink) use context with configurable timeouts.
- **Concurrency:** bounded worker pool (configurable size) — never spawn unbounded goroutines. Protect shared state (job store) with appropriate synchronization. Run with the race detector in tests.
- **HTTP clients:** construct one shared `*http.Client` with explicit timeouts per adapter and reuse it (connection pooling). Never use `http.DefaultClient` for outbound calls.
- **Comments:** explain *why*, not *what*. Comment non-obvious decisions and trade-offs; don't narrate trivial code.
- **Graceful shutdown:** in `main`, use `signal.NotifyContext` for SIGINT/SIGTERM; stop accepting new requests, cancel workers' context, drain in-flight jobs with a bounded timeout, then exit cleanly.

---

## 6. Security requirements

- **Auth:** protect `POST /v1/recordings` (and the status endpoint) with a bearer-token middleware; the expected token comes from config. Reject missing/invalid tokens with `401`.
- **Secrets:** only via environment variables. Never commit secrets. `.env` is git-ignored; provide `.env.example` with placeholder values.
- **Input validation:** enforce max upload size (also set Gin/`http` body limits, don't rely on app checks alone), validate audio content type against the allowlist, reject empty uploads.
- **Logging hygiene:** never log secrets, bearer tokens, full audio, or full transcripts at info level. Redact/omit sensitive fields; log identifiers and metadata instead.
- **Timeouts everywhere** on inbound server and outbound calls to bound resource use.
- Note TLS termination as a deployment concern in the README (out of scope to implement here).

---

## 7. Cost efficiency & scalability

- **Async ingest + bounded worker pool** so request latency is decoupled from LLM latency and concurrency/throughput are tunable via config.
- **Backpressure:** the queue is a bounded buffer; when full, the handler returns `503` (or `429`) rather than growing memory unboundedly. Make this behavior explicit.
- **Bounded memory:** cap upload size; stream the audio to the LLM adapter where the SDK/API supports it rather than copying large buffers unnecessarily.
- **Configurable model selection** so a cheaper model can be chosen; default to a cost-effective model.
- **Reuse clients/connections;** avoid per-request allocations in hot paths. Keep time/space complexity sensible — no needless copies or O(n²) handling of inputs.

---

## 8. Testing requirements

- Unit-test all **critical paths and branches**, using table-driven tests:
  - **Handlers:** rejects bad content type, rejects oversized/empty upload, rejects missing/invalid auth, happy path returns `202` with an id and enqueues; status endpoint returns correct states and `404` for unknown ids.
  - **Application/use-case service:** happy path; LLM error path; sink error path; retry-then-fail path; correct mapping into the canonical `Note`.
  - **Queue/worker:** job is processed, context cancellation stops workers, behavior when buffer is full.
  - **Config:** loads defaults, parses env, fails validation when required values are missing/invalid.
  - **Domain value objects:** construction validation.
- Use the port interfaces with **hand-written mocks** to isolate units (no real network calls in tests).
- Tests must pass with `go test -race ./...`. Aim for meaningful coverage on the areas above (quality over a vanity percentage).

---

## 9. Configuration (document every variable in `.env.example` and README)

Provide sensible `const` defaults; only secrets are mandatory.

- Server: `SERVER_PORT`, `SERVER_READ_TIMEOUT`, `SERVER_WRITE_TIMEOUT`, `SERVER_SHUTDOWN_TIMEOUT`
- Auth: `API_AUTH_TOKEN`
- Upload: `MAX_UPLOAD_BYTES`, `ALLOWED_AUDIO_TYPES` (comma-separated MIME types)
- Queue/workers: `QUEUE_BUFFER_SIZE`, `WORKER_COUNT`, `WORKER_MAX_RETRIES`, `WORKER_RETRY_BACKOFF`
- LLM: `LLM_PROVIDER`, `LLM_API_KEY`, `LLM_BASE_URL`, `LLM_TRANSCRIBE_MODEL`, `LLM_EXTRACT_MODEL`, `LLM_TIMEOUT`
- Sink: `SINK_TYPE` (`webhook` | `stdout` | `googlesheets`), `SINK_TIMEOUT`, `SINK_WEBHOOK_URL`; if Sheets: `GOOGLE_SHEETS_ID`, `GOOGLE_APPLICATION_CREDENTIALS`
- Logging: `LOG_LEVEL`, `LOG_FORMAT` (`json` | `text`)

Config is loaded and validated **once** at startup; fail fast with a clear error (in `main`) if required values are missing.

---

## 10. Docker & tooling

- **Multi-stage `Dockerfile`:** build stage on the official Go image; final stage on a minimal base (distroless or alpine). Run as a **non-root** user. Produce a small static binary.
- **`docker-compose.yml`:** brings the service up with env from `.env`; default to the `stdout` or `webhook` sink so it runs with **no external accounts**. The whole thing must start with a single command.
- **`Makefile`** with at least: `run`, `test`, `test-race`, `lint` (gofmt/go vet, golangci-lint if available), `build`, `docker-build`, `docker-up`, `docker-down`.
- **`.gitignore`** must exclude `.env`, build artifacts, and any credentials.

---

## 11. README requirements

Keep it short and action-oriented. It must include:
- One-paragraph overview of what the service does and the audio → LLM → sink flow.
- A simple architecture description and why ports/adapters were chosen (call out that LLM and sink are swappable).
- **Prerequisites** and **all commands** to run locally (with and without Docker) — copy-paste ready.
- A **sample `curl`** that uploads an audio file to `POST /v1/recordings` and a sample for checking status.
- The full table of environment variables with defaults.
- A short **"Design decisions & trade-offs"** section, including what was intentionally left out and why (see Non-Goals).

---

## 12. Non-Goals (do NOT build these — staying lean is part of the grade)

- **No external message broker** (Kafka/RabbitMQ/Redis). The in-memory queue behind an interface is sufficient; note in the README that swapping to SQS/Redis is a one-adapter change.
- **No database.** Job state lives in the in-memory store. Note its non-persistence as a known trade-off.
- **No Kubernetes/Helm/Terraform.** Docker + compose is the deployment surface for this exercise.
- **No multiple LLM adapters.** One solid reference adapter behind the port.
- **No auth framework, user management, or multi-tenancy.** A single bearer token is enough.
- **No heavy observability stack.** Structured logs + a request-id are enough; a `/metrics` endpoint is an optional stretch only if cheap.
- Do not add abstractions for variation that does not yet exist. Introduce an interface when there is a real second implementation or a real need to mock — not speculatively.

---

## 13. Definition of Done (self-check before finishing)

- `go build ./...` and `go vet ./...` are clean; code is `gofmt`-formatted.
- `go test -race ./...` passes, covering the critical paths in §8.
- `docker compose up` starts the service and it processes an audio upload end-to-end using a no-account sink.
- README contains every command needed to build, test, run (local + Docker), and a working `curl` example.
- No secrets committed; `.env` ignored; `.env.example` complete.
- No `log.Fatal`/`panic`/`os.Exit` outside `main`; errors are wrapped and handled.
- Every goroutine and external call receives a `context`.
- All tunables are config-driven; fixed defaults are `const`s.

---

## 14. Suggested build order

1. `go.mod`, config package (+ tests), domain entities/value objects/ports (+ VO tests).
2. In-memory queue + worker pool (+ tests) and in-memory job store.
3. Application/use-case service wiring AudioProcessor → NoteSink with retry (+ tests using mocks).
4. LLM adapter (OpenAI reference) and sink adapters (stdout + webhook; Sheets optional).
5. Gin transport: router, DTOs, middleware (auth, request-id, recovery), handlers (+ tests).
6. `main.go` composition root + graceful shutdown.
7. Dockerfile, docker-compose, Makefile, `.env.example`, `.gitignore`.
8. README. Final self-check against §13.
