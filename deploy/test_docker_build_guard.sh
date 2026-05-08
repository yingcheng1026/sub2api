#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
GUARD="${SCRIPT_DIR}/docker_build_guard.sh"

fail() {
    echo "FAIL: $*" >&2
    exit 1
}

assert_eq() {
    local expected="$1"
    local actual="$2"
    local label="$3"

    [[ "${actual}" == "${expected}" ]] || fail "${label}: expected '${expected}', got '${actual}'"
}

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

FAKE_DOCKER="${TMP_DIR}/docker"
CALL_LOG="${TMP_DIR}/docker.calls"

cat > "${FAKE_DOCKER}" <<'SH'
#!/usr/bin/env sh
printf '%s\n' "$*" >> "${SUB2API_FAKE_DOCKER_CALL_LOG}"
exit 0
SH
chmod +x "${FAKE_DOCKER}"

export SUB2API_REAL_DOCKER="${FAKE_DOCKER}"
export SUB2API_FAKE_DOCKER_CALL_LOG="${CALL_LOG}"
export SUB2API_BUILD_GUARD_ROOTS="${TMP_DIR}/default-guard-root"

expect_blocked() {
    local label="$1"
    shift

    : > "${CALL_LOG}"
    if "$@" >"${TMP_DIR}/${label}.out" 2>"${TMP_DIR}/${label}.err"; then
        fail "${label}: command should have been blocked"
    fi

    grep -q "Refusing direct Sub2API production Docker build" "${TMP_DIR}/${label}.err" || \
        fail "${label}: missing refusal message"
    [[ ! -s "${CALL_LOG}" ]] || fail "${label}: real docker should not be called"
}

expect_allowed() {
    local label="$1"
    local expected="$2"
    shift 2

    : > "${CALL_LOG}"
    "$@" >"${TMP_DIR}/${label}.out" 2>"${TMP_DIR}/${label}.err"
    assert_eq "${expected}" "$(cat "${CALL_LOG}")" "${label}: docker call"
}

expect_blocked direct_build "${GUARD}" build -t hfc/sub2api:guard-test .
expect_blocked registry_build "${GUARD}" build --tag registry.example.com/hfc/sub2api:guard-test .
expect_blocked buildx_build "${GUARD}" buildx build -t hfc/sub2api:guard-test .

expect_allowed non_sub2api_build "build -t local/test:guard ." \
    "${GUARD}" build -t local/test:guard .

expect_allowed build_image_bypass "build -t hfc/sub2api:guard-test ." \
    env SUB2API_BUILD_IMAGE_SH=1 "${GUARD}" build -t hfc/sub2api:guard-test .

GUARDED_ROOT="${TMP_DIR}/sub2api-fork"
mkdir -p "${GUARDED_ROOT}"

(
    cd "${GUARDED_ROOT}"
    export SUB2API_BUILD_GUARD_ROOTS="${GUARDED_ROOT}"
    expect_blocked guarded_checkout_build "${GUARD}" build -t local/test:guard .
    expect_blocked guarded_compose_build "${GUARD}" compose up -d --build
)

expect_allowed builder_prune "builder prune --force --max-used-space 5GB" \
    "${GUARD}" builder prune --force --max-used-space 5GB

echo "PASS: docker build guard tests"
