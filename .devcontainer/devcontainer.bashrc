#! /bin/bash
# shellcheck source=/dev/null

source /opt/conda/etc/profile.d/conda.sh
conda activate tyger

PATH=${PATH}:${HOME}/go/bin

source <(kubectl completion bash)
alias make="make -s"

if [[ "${BASH_ENV:-}" == "$(readlink -f "${BASH_SOURCE[0]:-}")" ]]; then
    # We don't want subshells to unnecessarily source this again.
    unset BASH_ENV
fi
