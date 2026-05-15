#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 1 ]]; then
  echo "Usage: $0 hfc/cursor-sidecar:<tag> [workdir]" >&2
  exit 2
fi

IMAGE_TAG="$1"
WORKDIR="${2:-/tmp/auth2api-cursor-sidecar-build}"
AUTH2API_REPO="${AUTH2API_REPO:-https://github.com/AmazingAng/auth2api.git}"
AUTH2API_COMMIT="${AUTH2API_COMMIT:-840fa100e71a3562552cc7d0267f7668db0d7f86}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PATCH_FILE="${SCRIPT_DIR}/auth2api-account-admin.patch"

rm -rf "${WORKDIR}"
git clone --quiet "${AUTH2API_REPO}" "${WORKDIR}"
git -C "${WORKDIR}" checkout --quiet "${AUTH2API_COMMIT}"
git -C "${WORKDIR}" apply "${PATCH_FILE}"

npm --prefix "${WORKDIR}" ci
npm --prefix "${WORKDIR}" run build
npm --prefix "${WORKDIR}" test -- --test-name-pattern "(admin/accounts/cursor|X-Cursor-Account-Ref)"

DOCKER_DEFAULT_PLATFORM="${DOCKER_DEFAULT_PLATFORM:-linux/amd64}" \
  docker build -t "${IMAGE_TAG}" "${WORKDIR}"

echo "Built ${IMAGE_TAG}"
