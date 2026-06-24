# syntax=docker/dockerfile:1
#
# Multi-stage build: the Go toolchain only ever runs inside these stages, so the
# host needs nothing but Docker. Stages:
#   builder  - shared base with modules downloaded (good layer caching)
#   test     - hermetic `go vet` + race-enabled tests; fails the build on failure
#   compile  - static, stripped binary (CGO disabled) for a distroless runtime
#   runtime  - distroless, non-root, no shell

# ---- builder ---------------------------------------------------------------
FROM golang:1.25-bookworm AS builder
WORKDIR /src
# Copy manifests first so dependency download is cached independently of source.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY . .

# ---- test ------------------------------------------------------------------
# `docker build --target test` (what `make test` runs) executes the suite during
# the build; any vet or test failure aborts the build. -race needs CGO + gcc,
# both present in the golang image.
FROM builder AS test
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go vet ./... && go test ./... -race -cover

# ---- compile ---------------------------------------------------------------
FROM builder AS compile
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/voiceline ./cmd/server

# ---- runtime ---------------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot AS runtime
WORKDIR /
COPY --from=compile /out/voiceline /voiceline
EXPOSE 8080
USER nonroot:nonroot
# distroless has no shell, so the container health probe reuses the binary's own
# `healthcheck` subcommand (exec form, no shell needed).
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD ["/voiceline", "healthcheck"]
ENTRYPOINT ["/voiceline"]
