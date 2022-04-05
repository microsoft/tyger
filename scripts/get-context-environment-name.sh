#! /bin/bash

set -euo pipefail

if [[ -n "${TYGER_ENVIRONMENT_NAME:-}" ]]; then
    echo "${TYGER_ENVIRONMENT_NAME:-}"
    exit
fi

if [[ ! "$(git config user.email)" =~ [^@]+ ]]; then
    echo "git email is not set"
    exit 1
fi

echo "${BASH_REMATCH[0]//[.\-_]/}"
