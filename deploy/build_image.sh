#!/usr/bin/env bash
# 本地/生产构建镜像的快速脚本，避免在命令行反复输入构建参数。

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

parse_image_ref() {
    local image="$1"
    local last_part="${image##*/}"

    if [[ "${last_part}" == *:* ]]; then
        printf '%s\t%s\n' "${image%:*}" "${image##*:}"
    else
        printf '%s\t%s\n' "${image}" "latest"
    fi
}

derive_feature_prefix() {
    local tag="$1"

    if [[ -z "${tag}" || "${tag}" == "latest" || "${tag}" == "<none>" ]]; then
        return 1
    fi

    if [[ "${tag}" =~ ^(.+)-[0-9a-f]{7,40}-[0-9]{8}(-[0-9]{4,6})?$ ]]; then
        printf '%s\n' "${BASH_REMATCH[1]}"
        return 0
    fi

    if [[ "${tag}" =~ ^(.+)-[0-9]{8}(-[0-9]{4,6})?$ ]]; then
        printf '%s\n' "${BASH_REMATCH[1]}"
        return 0
    fi

    return 1
}

cleanup_old_feature_images() {
    local repository="$1"
    local current_tag="$2"
    local keep="${SUB2API_IMAGE_KEEP:-3}"
    local cleanup_enabled="${SUB2API_IMAGE_CLEANUP:-1}"
    local prefix="${SUB2API_IMAGE_PREFIX:-}"
    local tag_list
    local tag
    local count=0

    if [[ "${cleanup_enabled}" == "0" || "${cleanup_enabled}" == "false" ]]; then
        echo "Image cleanup disabled by SUB2API_IMAGE_CLEANUP=${cleanup_enabled}."
        return 0
    fi

    if ! [[ "${keep}" =~ ^[0-9]+$ ]] || (( keep < 1 )); then
        echo "SUB2API_IMAGE_KEEP must be a positive integer, got: ${keep}" >&2
        return 1
    fi

    if [[ -z "${prefix}" ]]; then
        if ! prefix="$(derive_feature_prefix "${current_tag}")"; then
            echo "Skip image cleanup: tag '${current_tag}' has no feature prefix."
            return 0
        fi
    fi

    if ! tag_list="$(docker image ls "${repository}" --format '{{.Tag}}')"; then
        echo "Warning: unable to list Docker images for ${repository}; skipping cleanup." >&2
        return 0
    fi

    while IFS= read -r tag; do
        [[ -z "${tag}" || "${tag}" == "<none>" ]] && continue
        [[ "${tag}" == "${prefix}-"* ]] || continue

        count=$((count + 1))
        if (( count <= keep )); then
            continue
        fi

        if [[ "${SUB2API_IMAGE_CLEANUP_DRY_RUN:-0}" == "1" ]]; then
            echo "DRY RUN: docker image rm ${repository}:${tag}"
        else
            echo "Removing old image tag ${repository}:${tag}"
            docker image rm "${repository}:${tag}" || \
                echo "Warning: failed to remove ${repository}:${tag}; it may still be in use." >&2
        fi
    done <<< "${tag_list}"
}

prune_builder_cache() {
    local gc_enabled="${SUB2API_BUILDER_GC:-1}"
    local keep_storage="${SUB2API_BUILDER_KEEP_STORAGE:-5GB}"
    local help_text
    local prune_args=(--force)

    if [[ "${gc_enabled}" == "0" || "${gc_enabled}" == "false" ]]; then
        echo "Build cache GC disabled by SUB2API_BUILDER_GC=${gc_enabled}."
        return 0
    fi

    help_text="$(docker builder prune --help 2>&1 || true)"
    if [[ "${help_text}" == *"--keep-storage"* ]]; then
        prune_args+=(--keep-storage "${keep_storage}")
    elif [[ "${help_text}" == *"--max-used-space"* ]]; then
        prune_args+=(--max-used-space "${keep_storage}")
    else
        echo "Warning: Docker builder prune has no cache-size flag; pruning prompt-only cache." >&2
    fi

    if [[ "${SUB2API_BUILDER_GC_DRY_RUN:-0}" == "1" ]]; then
        echo "DRY RUN: docker builder prune ${prune_args[*]}"
        return 0
    fi

    echo "Pruning Docker build cache with limit ${keep_storage}"
    docker builder prune "${prune_args[@]}" || \
        echo "Warning: failed to prune Docker build cache." >&2
}

assert_account_test_modal_initial_load_guard() {
    local modal="${REPO_ROOT}/frontend/src/components/admin/account/AccountTestModal.vue"
    local spec="${REPO_ROOT}/frontend/src/components/admin/account/__tests__/AccountTestModal.spec.ts"

    if ! grep -q "immediate: true" "${modal}"; then
        echo "AccountTestModal initial-open model load guard failed: missing immediate watcher option." >&2
        return 1
    fi

    if ! grep -Fq "props.account?.id" "${modal}"; then
        echo "AccountTestModal model load guard failed: watcher must include account id changes." >&2
        return 1
    fi

    if ! grep -q "getAvailableModels).toHaveBeenCalledWith(42)" "${spec}"; then
        echo "AccountTestModal initial-open model load guard failed: missing regression assertion." >&2
        return 1
    fi

    if ! grep -q "getAvailableModels).toHaveBeenNthCalledWith(2, 84)" "${spec}"; then
        echo "AccountTestModal account-switch model load guard failed: missing regression assertion." >&2
        return 1
    fi
}


assert_account_stats_modal_initial_load_guard() {
    local modal="${REPO_ROOT}/frontend/src/components/admin/account/AccountStatsModal.vue"
    local spec="${REPO_ROOT}/frontend/src/components/admin/account/__tests__/AccountStatsModal.spec.ts"

    if ! grep -q "immediate: true" "${modal}"; then
        echo "AccountStatsModal initial-open stats load guard failed: missing immediate watcher option." >&2
        return 1
    fi

    if ! grep -q "await loadStats()" "${modal}"; then
        echo "AccountStatsModal initial-open stats load guard failed: watcher must call loadStats." >&2
        return 1
    fi

    if ! grep -q "getStats).toHaveBeenCalledWith(86, 30)" "${spec}"; then
        echo "AccountStatsModal initial-open stats load guard failed: missing regression assertion." >&2
        return 1
    fi

    if ! grep -q "await wrapper.setProps({ show: true })" "${spec}"; then
        echo "AccountStatsModal reopen stats load guard failed: missing reopen regression assertion." >&2
        return 1
    fi
}

main() {
    local image="${SUB2API_IMAGE:-${IMAGE:-}}"
    local repository
    local tag

    if [[ $# -gt 0 && "$1" != -* ]]; then
        image="$1"
        shift
    fi

    if [[ -z "${image}" ]]; then
        repository="${SUB2API_IMAGE_REPOSITORY:-${IMAGE_REPOSITORY:-sub2api}}"
        tag="${SUB2API_IMAGE_TAG:-${IMAGE_TAG:-latest}}"
        image="${repository}:${tag}"
    else
        read -r repository tag < <(parse_image_ref "${image}")
    fi

    assert_account_test_modal_initial_load_guard
    assert_account_stats_modal_initial_load_guard

    SUB2API_BUILD_IMAGE_SH=1 docker build -t "${image}" \
        --build-arg GOPROXY=https://goproxy.cn,direct \
        --build-arg GOSUMDB=sum.golang.google.cn \
        "$@" \
        -f "${REPO_ROOT}/Dockerfile" \
        "${REPO_ROOT}"

    cleanup_old_feature_images "${repository}" "${tag}"
    prune_builder_cache
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
    main "$@"
fi
