#!/usr/bin/env bash
# Guard production Sub2API Docker builds so they go through deploy/build_image.sh.

set -euo pipefail

find_real_docker() {
    if [[ -n "${SUB2API_REAL_DOCKER:-}" ]]; then
        printf '%s\n' "${SUB2API_REAL_DOCKER}"
        return 0
    fi

    local candidate
    for candidate in /usr/bin/docker /bin/docker /usr/local/bin/docker.real; do
        if [[ -x "${candidate}" && "${candidate}" != "${BASH_SOURCE[0]}" ]]; then
            printf '%s\n' "${candidate}"
            return 0
        fi
    done

    echo "Unable to find the real docker binary. Set SUB2API_REAL_DOCKER." >&2
    return 1
}

is_truthy() {
    case "${1:-}" in
        1|true|TRUE|yes|YES|on|ON) return 0 ;;
        *) return 1 ;;
    esac
}

arg_has_sub2api_image_tag() {
    local arg
    for arg in "$@"; do
        case "${arg}" in
            hfc/sub2api:*|*/hfc/sub2api:*|--tag=hfc/sub2api:*|--tag=*/hfc/sub2api:*|-t=hfc/sub2api:*|-t=*/hfc/sub2api:*)
                return 0
                ;;
        esac
    done
    return 1
}

in_guarded_sub2api_checkout() {
    local pwd_real
    local root
    local roots="${SUB2API_BUILD_GUARD_ROOTS:-/opt/relay/sub2api-fork:/opt/relay/ai-relay-infra/sub2api}"

    pwd_real="$(pwd -P)"
    IFS=':' read -r -a root_array <<< "${roots}"
    for root in "${root_array[@]}"; do
        [[ -z "${root}" ]] && continue
        if [[ -d "${root}" ]]; then
            root="$(cd "${root}" && pwd -P)"
        fi
        case "${pwd_real}" in
            "${root}"|"${root}"/*) return 0 ;;
        esac
    done

    return 1
}

reject_direct_build() {
    cat >&2 <<'EOF'
Refusing direct Sub2API production Docker build.

Use deploy/build_image.sh so old same-feature image tags are pruned and BuildKit
cache is capped after a successful build, for example:

  ./deploy/build_image.sh "hfc/sub2api:<feature>-$(git rev-parse --short=12 HEAD)-$(date +%Y%m%d-%H%M%S)"

Emergency bypass is possible with SUB2API_DOCKER_BUILD_GUARD_BYPASS=1, but only
when equivalent image-tag cleanup and builder-cache GC are done in the same task.
EOF
    return 64
}

should_reject_build() {
    if is_truthy "${SUB2API_BUILD_IMAGE_SH:-}" || is_truthy "${SUB2API_DOCKER_BUILD_GUARD_BYPASS:-}"; then
        return 1
    fi

    if arg_has_sub2api_image_tag "$@" || in_guarded_sub2api_checkout; then
        return 0
    fi

    return 1
}

should_reject_compose_build() {
    local arg

    if is_truthy "${SUB2API_BUILD_IMAGE_SH:-}" || is_truthy "${SUB2API_DOCKER_BUILD_GUARD_BYPASS:-}"; then
        return 1
    fi

    in_guarded_sub2api_checkout || return 1

    for arg in "$@"; do
        [[ "${arg}" == "--build" ]] && return 0
    done

    return 1
}

main() {
    local real_docker
    real_docker="$(find_real_docker)"

    if [[ $# -eq 0 ]]; then
        exec "${real_docker}"
    fi

    case "$1" in
        build)
            if should_reject_build "${@:2}"; then
                reject_direct_build
            fi
            ;;
        buildx)
            if [[ "${2:-}" == "build" ]] && should_reject_build "${@:3}"; then
                reject_direct_build
            fi
            ;;
        compose)
            if should_reject_compose_build "${@:2}"; then
                reject_direct_build
            fi
            ;;
    esac

    exec "${real_docker}" "$@"
}

main "$@"
