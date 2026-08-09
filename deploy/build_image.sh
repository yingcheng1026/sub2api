#!/usr/bin/env bash
# Build a tagged image through the repository-root Dockerfile.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

image="sub2api:latest"
if [[ $# -gt 0 && "$1" != -* ]]; then
    image="$1"
    shift
fi

docker build \
    -t "${image}" \
    "$@" \
    --build-arg GOPROXY=https://goproxy.cn,direct \
    --build-arg GOSUMDB=sum.golang.google.cn \
    -f "${REPO_ROOT}/Dockerfile" \
    "${REPO_ROOT}"
