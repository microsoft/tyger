#! /bin/bash
# shellcheck source=/dev/null

PATH=${PATH}:${HOME}/go/bin

source <(kubectl completion bash)
if command -v tyger &> /dev/null; then
    source <(tyger completion bash)
fi
if command -v tyger-proxy &> /dev/null; then
    source <(tyger-proxy completion bash)
fi
alias make="make -s -j"

if [[ "${BASH_ENV:-}" == "$(readlink -f "${BASH_SOURCE[0]:-}")" ]]; then
    # We don't want subshells to unnecessarily source this again.
    unset BASH_ENV
fi

if [[ "$(pwd)" == "/workspaces/tyger-docker" ]]; then
    export TYGER_ENVIRONMENT_TYPE="docker"
    export TYGER_CACHE_FILE="${XDG_CACHE_HOME:-${HOME}/.cache}/tyger/.tyger-docker"
fi
