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
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
    -o /out/vlr-main-agent ./cmd/vlr-main-agent

# ---- runtime ----
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/vlr-main-agent /usr/local/bin/vlr-main-agent
USER 65532:65532
EXPOSE 8443
# Self-probe via the binary (distroless has no shell/curl).
HEALTHCHECK --interval=15s --timeout=4s --start-period=10s --retries=3 \
    CMD ["/usr/local/bin/vlr-main-agent", "healthz"]
ENTRYPOINT ["/usr/local/bin/vlr-main-agent"]
