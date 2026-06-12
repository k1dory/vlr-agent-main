# syntax=docker/dockerfile:1
# Multistage build for vlr-main-agent: a static binary on a distroless nonroot
# base. No shell, no package manager, runs as uid 65532.

# ---- build ----
FROM golang:1.25-alpine AS build
WORKDIR /src
# Cache modules first (better layer reuse than copying everything up front).
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
# GO_BUILD_PAR caps build parallelism (and GOMAXPROCS) to keep peak RAM low on a
# small VPS, where the full-parallelism clickhouse-go compile can thrash swap or
# OOM. 0 = use all cores (default, fastest). Set to 1 on a ~1GB box:
#   GO_BUILD_PAR=1 docker compose -f deploy/compose/docker-compose.yml up -d --build
ARG GO_BUILD_PAR=0
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    sh -euc 'P=""; if [ "${GO_BUILD_PAR}" != "0" ]; then export GOMAXPROCS="${GO_BUILD_PAR}"; P="-p=${GO_BUILD_PAR}"; fi; \
    CGO_ENABLED=0 GOOS=linux go build $P -trimpath -ldflags="-s -w" -o /out/vlr-main-agent ./cmd/vlr-main-agent'

# ---- runtime ----
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/vlr-main-agent /usr/local/bin/vlr-main-agent
USER 65532:65532
EXPOSE 8443
# Self-probe via the binary (distroless has no shell/curl).
HEALTHCHECK --interval=15s --timeout=4s --start-period=10s --retries=3 \
    CMD ["/usr/local/bin/vlr-main-agent", "healthz"]
ENTRYPOINT ["/usr/local/bin/vlr-main-agent"]
