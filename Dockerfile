# =============================================================================
# Sub2API Multi-Stage Dockerfile
# =============================================================================
# Stage 1: Build frontend
# Stage 2: Build Go backend with embedded frontend
# Stage 3: Final minimal image
# =============================================================================

ARG NODE_IMAGE=node:24-alpine
ARG GOLANG_IMAGE=golang:1.26.3-alpine
ARG ALPINE_IMAGE=alpine:3.21
ARG POSTGRES_IMAGE=postgres:18-alpine
ARG GOPROXY=https://goproxy.cn,direct
ARG GOSUMDB=sum.golang.google.cn

# -----------------------------------------------------------------------------
# Stage 1: Frontend Builder
# -----------------------------------------------------------------------------
# --platform=$BUILDPLATFORM: 在 host 原生架构跑 (Apple Silicon arm64 / Intel amd64
# 都不走 QEMU emulation),避免 esbuild 在 QEMU 下 crash。frontend dist 是纯静态
# 文件,跟最终 image 架构无关。
FROM --platform=$BUILDPLATFORM ${NODE_IMAGE} AS frontend-builder

WORKDIR /app/frontend

# Install pnpm
# 固定 pnpm 9.x: pnpm@10 启用了对未批准 install scripts 的严格拒绝
# (ERR_PNPM_IGNORED_BUILDS), 会阻塞 esbuild/vue-demi 的构建。
# 等 frontend/package.json 加上 pnpm.onlyBuiltDependencies 白名单后再升级。
RUN corepack enable && corepack prepare pnpm@9.15.4 --activate

# Copy frontend source and build.
# Keep install/build/cleanup in one layer so low-disk production hosts do not
# have to commit a large node_modules-only layer.
COPY frontend/package.json frontend/pnpm-lock.yaml ./
COPY frontend/ ./
# FRONTEND_NODE_OPTIONS 可调: 默认 3072 适合本地 build (Mac / 大内存机);
# VPS build 时降到 --max-old-space-size=1024 或 768 避免 OOM。
ARG FRONTEND_NODE_OPTIONS="--max-old-space-size=3072"
RUN pnpm install --frozen-lockfile && \
    NODE_OPTIONS="${FRONTEND_NODE_OPTIONS}" pnpm run build && \
    rm -rf node_modules /root/.cache /root/.local/share/pnpm

# -----------------------------------------------------------------------------
# Stage 2: Backend Builder
# -----------------------------------------------------------------------------
# --platform=$BUILDPLATFORM: host 原生跑 go (避免 QEMU 拖慢 ~3 倍);
# 配合 GOARCH=$TARGETARCH 跨编译到目标架构,产 amd64 binary 塞进 amd64 final image。
FROM --platform=$BUILDPLATFORM ${GOLANG_IMAGE} AS backend-builder

# Build arguments for version info (set by CI)
ARG VERSION=
ARG COMMIT=docker
ARG DATE
ARG GOPROXY
ARG GOSUMDB
ARG GO_BUILD_GOMAXPROCS=
ARG GO_BUILD_FLAGS=
# buildx auto-injects TARGETARCH when --platform is set (e.g. linux/amd64 → amd64)
ARG TARGETARCH

ENV GOPROXY=${GOPROXY}
ENV GOSUMDB=${GOSUMDB}

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /app/backend

# Copy go mod files first (better caching)
COPY backend/go.mod backend/go.sum ./
RUN go mod download

# Copy backend source first
COPY backend/ ./

# Copy frontend dist from previous stage (must be after backend copy to avoid being overwritten)
COPY --from=frontend-builder /app/backend/internal/web/dist ./internal/web/dist

# Build the binary (BuildType=release for CI builds, embed frontend)
# Version precedence: build arg VERSION > cmd/server/VERSION
RUN VERSION_VALUE="${VERSION}" && \
    if [ -z "${VERSION_VALUE}" ]; then VERSION_VALUE="$(tr -d '\r\n' < ./cmd/server/VERSION)"; fi && \
    DATE_VALUE="${DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}" && \
    if [ -n "${GO_BUILD_GOMAXPROCS}" ]; then export GOMAXPROCS="${GO_BUILD_GOMAXPROCS}"; fi && \
    CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH:-amd64} go build \
    ${GO_BUILD_FLAGS} \
    -tags embed \
    -ldflags="-s -w -X main.Version=${VERSION_VALUE} -X main.Commit=${COMMIT} -X main.Date=${DATE_VALUE} -X main.BuildType=release" \
    -trimpath \
    -o /app/sub2api \
    ./cmd/server

# -----------------------------------------------------------------------------
# Stage 3: PostgreSQL Client (version-matched with docker-compose)
# -----------------------------------------------------------------------------
FROM ${POSTGRES_IMAGE} AS pg-client

# -----------------------------------------------------------------------------
# Stage 4: Final Runtime Image
# -----------------------------------------------------------------------------
FROM ${ALPINE_IMAGE}

# Labels
LABEL maintainer="Wei-Shaw <github.com/Wei-Shaw>"
LABEL description="Sub2API - AI API Gateway Platform"
LABEL org.opencontainers.image.source="https://github.com/Wei-Shaw/sub2api"

# Install runtime dependencies
RUN apk add --no-cache \
    ca-certificates \
    tzdata \
    su-exec \
    libpq \
    zstd-libs \
    lz4-libs \
    krb5-libs \
    libldap \
    libedit \
    && rm -rf /var/cache/apk/*

# Copy pg_dump and psql from the same postgres image used in docker-compose
# This ensures version consistency between backup tools and the database server
COPY --from=pg-client /usr/local/bin/pg_dump /usr/local/bin/pg_dump
COPY --from=pg-client /usr/local/bin/psql /usr/local/bin/psql
COPY --from=pg-client /usr/local/lib/libpq.so.5* /usr/local/lib/

# Create non-root user
RUN addgroup -g 1000 sub2api && \
    adduser -u 1000 -G sub2api -s /bin/sh -D sub2api

# Set working directory
WORKDIR /app

# Copy binary/resources with ownership to avoid extra full-layer chown copy
COPY --from=backend-builder --chown=sub2api:sub2api /app/sub2api /app/sub2api
COPY --from=backend-builder --chown=sub2api:sub2api /app/backend/resources /app/resources

# Create data directory and stamp the production fix set required by compose health checks.
RUN mkdir -p /app/data && \
    printf '%s\n' \
        'monthly-cover-empty-guard=402ce708' \
        'account-stats-modal-guard=6cc80e62' \
        'group-availability-count=20260524' \
        > /app/.hfc-prod-fixset-20260524 && \
    chown sub2api:sub2api /app/data /app/.hfc-prod-fixset-20260524

# Copy entrypoint script (fixes volume permissions then drops to sub2api)
COPY deploy/docker-entrypoint.sh /app/docker-entrypoint.sh
RUN chmod +x /app/docker-entrypoint.sh

# Expose port (can be overridden by SERVER_PORT env var)
EXPOSE 8080

# Health check
HEALTHCHECK --interval=30s --timeout=10s --start-period=10s --retries=3 \
    CMD test -f /app/.hfc-prod-fixset-20260524 \
        && grep -q 'monthly-cover-empty-guard=402ce708' /app/.hfc-prod-fixset-20260524 \
        && grep -q 'account-stats-modal-guard=6cc80e62' /app/.hfc-prod-fixset-20260524 \
        && grep -q 'group-availability-count=20260524' /app/.hfc-prod-fixset-20260524 \
        && wget -q -T 5 -O /dev/null http://localhost:${SERVER_PORT:-8080}/health || exit 1

# Run the application (entrypoint fixes /app/data ownership then execs as sub2api)
ENTRYPOINT ["/app/docker-entrypoint.sh"]
CMD ["/app/sub2api"]
