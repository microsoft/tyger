#!/bin/bash

set -eu

"$(dirname "$0")/check-login.sh"

if [[ "$1" =~ ([^.]+).azurecr.io ]]; then
  acr="${BASH_REMATCH[1]}"
  container_registry_fqdn="$1"
else
  acr="$1"
  container_registry_fqdn="${acr}.azurecr.io"
fi

#######################################
# Decodes a string that is "Base64url" encoded.
# See https://www.rfc-editor.org/rfc/rfc7515.txt
# Arguments:
#   The string to decode
# Outputs:
#   Writes the decoded string to stdout
#######################################
decode_base64url() {
  local len_mod4=$((${#1} % 4))
  local result="$1"

  if [ $len_mod4 -eq 2 ]; then
    result="$1"'=='
  elif [ $len_mod4 -eq 3 ]; then
    result="$1"'='
  fi

  echo "$result" | tr '_-' '/+' | base64 -d
}

# Ensure we are logged in to the ACR resource, but avoid calling az acr login if an existing token is still valid.
token_file="${HOME}/.docker/config.json"
tokenExpiration=$([[ ! -f "${token_file}" ]] || decode_base64url "$(< "${token_file}" jq --arg registry "${container_registry_fqdn}" -r '.auths[$registry].identitytoken' | cut -d "." -f 2)" | jq .exp 2> /dev/null || true)
currentTime=$(date +%s)
if (((tokenExpiration - currentTime) < 900)); then
    az acr login -n "$acr"
fi
