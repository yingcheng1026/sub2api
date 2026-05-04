# Sub2API Docker Image

Sub2API is an AI API Gateway Platform for distributing and managing AI product subscription API quotas.

## Quick Start

```bash
docker run -d \
  --name sub2api \
  -p 8080:8080 \
  -e DATABASE_URL="postgres://user:pass@host:5432/sub2api" \
  -e REDIS_URL="redis://host:6379" \
  weishaw/sub2api:latest
```

## Docker Compose

```yaml
version: '3.8'

services:
  sub2api:
    image: weishaw/sub2api:latest
    ports:
      - "8080:8080"
    environment:
      - DATABASE_URL=postgres://postgres:postgres@db:5432/sub2api?sslmode=disable
      - REDIS_URL=redis://redis:6379
    depends_on:
      - db
      - redis

  db:
    image: postgres:15-alpine
    environment:
      - POSTGRES_USER=postgres
      - POSTGRES_PASSWORD=postgres
      - POSTGRES_DB=sub2api
    volumes:
      - postgres_data:/var/lib/postgresql/data

  redis:
    image: redis:7-alpine
    volumes:
      - redis_data:/data

volumes:
  postgres_data:
  redis_data:
```

## Environment Variables

| Variable | Description | Required | Default |
|----------|-------------|----------|---------|
| `DATABASE_URL` | PostgreSQL connection string | Yes | - |
| `REDIS_URL` | Redis connection string | Yes | - |
| `PORT` | Server port | No | `8080` |
| `GIN_MODE` | Gin framework mode (`debug`/`release`) | No | `release` |

## Supported Architectures

- `linux/amd64`
- `linux/arm64`

## Tags

- `latest` - Latest stable release
- `x.y.z` - Specific version
- `x.y` - Latest patch of minor version
- `x` - Latest minor of major version

## Building Feature Images

Use `deploy/build_image.sh` for production-style feature tags so old images from
the same feature line are removed after a successful build:

```bash
./deploy/build_image.sh "hfc/sub2api:chat-routing-$(git rev-parse --short=12 HEAD)-$(date +%Y%m%d-%H%M%S)"
```

By default the script keeps the newest 3 local tags with the same feature prefix,
for example `chat-routing-*`, removes older tags with `docker image rm` without
`--force`, and prunes Docker build cache with a 5GB cache limit using the
builder-prune size flag supported by the local Docker version. Override with:

```bash
SUB2API_IMAGE_KEEP=5 ./deploy/build_image.sh "hfc/sub2api:chat-routing-$(git rev-parse --short=12 HEAD)-$(date +%Y%m%d-%H%M%S)"
SUB2API_IMAGE_CLEANUP=0 ./deploy/build_image.sh "hfc/sub2api:manual-test-$(date +%Y%m%d-%H%M%S)"
SUB2API_BUILDER_KEEP_STORAGE=8GB ./deploy/build_image.sh "hfc/sub2api:chat-routing-$(git rev-parse --short=12 HEAD)-$(date +%Y%m%d-%H%M%S)"
SUB2API_BUILDER_GC=0 ./deploy/build_image.sh "hfc/sub2api:manual-test-$(date +%Y%m%d-%H%M%S)"
```

## Production Build Guard

Production hosts can install `deploy/docker_build_guard.sh` as a Docker CLI
wrapper, for example at `/usr/local/bin/docker` when `/usr/bin/docker` is the
real binary. The guard refuses direct `docker build` / `docker buildx build`
commands for `hfc/sub2api:*` and refuses `docker compose ... --build` inside the
production Sub2API checkouts. `deploy/build_image.sh` sets
`SUB2API_BUILD_IMAGE_SH=1` for its own build command, so the approved path keeps
working while accidental direct production builds are blocked.

Emergency bypass is `SUB2API_DOCKER_BUILD_GUARD_BYPASS=1`, but it must be paired
with equivalent same-feature image cleanup and builder-cache GC in the same task.

## Links

- [GitHub Repository](https://github.com/weishaw/sub2api)
- [Documentation](https://github.com/weishaw/sub2api#readme)
