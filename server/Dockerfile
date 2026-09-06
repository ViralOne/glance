# syntax=docker/dockerfile:1

# Both build stages are pinned to BUILDPLATFORM and cross-compile to
# TARGETPLATFORM, rather than running under emulation.
#
# This matters a lot for the arm64 image. Compiling Go under QEMU took ten
# minutes or more, most of it spent emulating the pure-Go SQLite driver, which
# is large. The driver is pure Go and CGO is off, so there is nothing to emulate
# for: setting GOARCH is enough, and the build runs at native speed on the
# amd64 runner. Node likewise emits platform-independent JavaScript, so the UI
# only needs building once.

# ---- web: build the Svelte UI (architecture independent) ----
FROM --platform=$BUILDPLATFORM node:24-alpine AS web
WORKDIR /src
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web .
RUN npm run build

# ---- build: cross-compile the Go binary with the UI embedded ----
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
COPY --from=web /internal/web/dist ./internal/web/dist
ARG VERSION=dev
# Supplied by buildx for each requested platform.
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build,sharing=locked \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w -X github.com/ViralOne/glance/server/internal/api.Version=${VERSION}" -o /glance ./cmd/glance

# ---- runtime: binary + CA certificates only ----
FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 1000 glance && mkdir -p /data && chown glance:glance /data
COPY --from=build /glance /usr/local/bin/glance
USER glance
VOLUME ["/data"]
EXPOSE 8080
ENV GLANCE_PORT=8080 GLANCE_DATABASE_PATH=/data/glance.db
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://127.0.0.1:8080/health || exit 1
ENTRYPOINT ["glance"]
