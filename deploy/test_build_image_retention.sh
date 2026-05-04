#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

source "${SCRIPT_DIR}/build_image.sh"

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

test_parse_image_ref() {
    local repository
    local tag

    read -r repository tag < <(parse_image_ref "hfc/sub2api:chat-routing-e2e8be417245-20260503-183747")
    assert_eq "hfc/sub2api" "${repository}" "repository with tag"
    assert_eq "chat-routing-e2e8be417245-20260503-183747" "${tag}" "tag with repository"

    read -r repository tag < <(parse_image_ref "registry.example.com:5000/hfc/sub2api:admin-sidebar-981a958-20260504-0000")
    assert_eq "registry.example.com:5000/hfc/sub2api" "${repository}" "repository with registry port"
    assert_eq "admin-sidebar-981a958-20260504-0000" "${tag}" "tag with registry port"

    read -r repository tag < <(parse_image_ref "sub2api")
    assert_eq "sub2api" "${repository}" "repository without tag"
    assert_eq "latest" "${tag}" "implicit latest tag"
}

test_derive_feature_prefix() {
    assert_eq "chat-routing" \
        "$(derive_feature_prefix "chat-routing-e2e8be417245-20260503-183747")" \
        "sha dated feature prefix"

    assert_eq "official-model-picker" \
        "$(derive_feature_prefix "official-model-picker-2ae377c-20260503-2055")" \
        "short time feature prefix"

    assert_eq "billing-requested-model" \
        "$(derive_feature_prefix "billing-requested-model-20260502-230014")" \
        "dated feature prefix without sha"

    if derive_feature_prefix "latest" >/dev/null; then
        fail "latest must not derive a feature prefix"
    fi

    if derive_feature_prefix "manual-test" >/dev/null; then
        fail "unstructured tags must not derive a feature prefix"
    fi
}

test_cleanup_keeps_three_matching_tags() {
    local -a removed=()

    docker() {
        if [[ "$1" == "image" && "$2" == "ls" ]]; then
            [[ "$3" == "hfc/sub2api" ]] || fail "unexpected image ls repository: $3"
            printf '%s\n' \
                "chat-routing-aaaaaaaaaaaa-20260504-010000" \
                "chat-routing-bbbbbbbbbbbb-20260503-030000" \
                "chat-routing-cccccccccccc-20260503-020000" \
                "chat-routing-dddddddddddd-20260503-010000" \
                "admin-sidebar-981a958-20260504-0000" \
                "<none>"
            return 0
        fi

        if [[ "$1" == "image" && "$2" == "rm" ]]; then
            removed[${#removed[@]}]="$3"
            return 0
        fi

        fail "unexpected docker command: $*"
    }

    SUB2API_IMAGE_CLEANUP=1 SUB2API_IMAGE_KEEP=3 \
        cleanup_old_feature_images "hfc/sub2api" "chat-routing-aaaaaaaaaaaa-20260504-010000" >/tmp/sub2api-image-retention-test.out

    assert_eq "1" "${#removed[@]}" "removed image count"
    assert_eq "hfc/sub2api:chat-routing-dddddddddddd-20260503-010000" "${removed[0]}" "oldest matching image removed"
}

test_cleanup_skips_latest() {
    local -a removed=()

    docker() {
        if [[ "$1" == "image" && "$2" == "rm" ]]; then
            removed[${#removed[@]}]="$3"
            return 0
        fi

        fail "latest cleanup should not call docker: $*"
    }

    cleanup_old_feature_images "hfc/sub2api" "latest" >/tmp/sub2api-image-retention-test.out
    assert_eq "0" "${#removed[@]}" "latest cleanup remove count"
}

test_builder_gc_uses_keep_storage() {
    local builder_prune_command=""

    docker() {
        if [[ "$1" == "builder" && "$2" == "prune" && "$3" == "--help" ]]; then
            echo "      --max-used-space bytes   Maximum amount of disk space allowed to keep for cache"
            return 0
        fi

        if [[ "$1" == "builder" && "$2" == "prune" ]]; then
            builder_prune_command="$*"
            return 0
        fi

        fail "unexpected docker command: $*"
    }

    SUB2API_BUILDER_GC=1 SUB2API_BUILDER_KEEP_STORAGE=5GB \
        prune_builder_cache >/tmp/sub2api-image-retention-test.out

    assert_eq "builder prune --force --max-used-space 5GB" "${builder_prune_command}" "builder prune command"
}

test_builder_gc_can_be_disabled() {
    local builder_prune_called=0

    docker() {
        builder_prune_called=1
        fail "builder GC disabled but docker was called: $*"
    }

    SUB2API_BUILDER_GC=0 prune_builder_cache >/tmp/sub2api-image-retention-test.out
    assert_eq "0" "${builder_prune_called}" "disabled builder prune call count"
}

test_parse_image_ref
test_derive_feature_prefix
test_cleanup_keeps_three_matching_tags
test_cleanup_skips_latest
test_builder_gc_uses_keep_storage
test_builder_gc_can_be_disabled

echo "PASS: build image retention tests"
