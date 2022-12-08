#! /bin/bash
# A simple script for testing tyger login scenarios

printf "\n===PREREQUISITES===\n\n"

make -f "$(dirname "$0")/../Makefile" install-cli
if ! make -f "$(dirname "$0")/../Makefile" check-test-client-cert; then
    make -f "$(dirname "$0")/../Makefile" download-test-client-cert
fi
make -f "$(dirname "$0")/../Makefile" login-service-principal
uri=$(make -s -f "$(dirname "$0")/../Makefile" get-tyger-uri)

set -euo pipefail

printf "\n===SERVICE PRINCIPAL LOGIN===\n\n"

make -f "$(dirname "$0")/../Makefile" login-service-principal
tyger login status

token_file="${XDG_CACHE_HOME:-$HOME/.cache}/tyger/.tyger"

# expire token
yq e -i '.lastTokenExpiration = 30' "${token_file}"
tyger login status

printf "\n===DEVICE CODE LOGIN===\n\n"

tyger login "${uri}" --use-device-code
tyger login status

# expire token
yq e -i '.lastTokenExpiration = 30' "${token_file}"
tyger login status

printf "\n===INTERACTIVE LOGIN===\n\n"

tyger login "${uri}"
tyger login status

# expire token
yq e -i '.lastTokenExpiration = 30' "${token_file}"
tyger login status

printf "\n===INTERACTIVE FALLING BACK TO DEVICE CODE===\n\n"

restricted_path=$(dirname "$(which tyger)")
grep=$(which grep)
tee=$(which tee)
export PATH="$restricted_path"
unset BROWSER

contains_devicelogin=$(tyger login "${uri}" | $tee /dev/tty | { $grep /devicelogin || true; })
if [[ -z "${contains_devicelogin}" ]]; then
    echo "Failed to fall back to device login"
    exit 1
fi

tyger login status
