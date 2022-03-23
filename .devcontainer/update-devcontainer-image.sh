#!/bin/bash
#
# Run this script when you make changes to the "devcontainer" target of the Dockerfile in the root of this repo.
# It builds the image, pushes it a container registry, and updates the "image" property in the devcontainer.json field.

set -euo pipefail
# shellcheck source=envrc
source "$(dirname "$0")/../envrc"

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

# Build the image

this_dir=$(dirname "$0")
existing_image=$("${this_dir}/get-devcontainer-image.sh")

docker build -f "${this_dir}/Dockerfile" --target devcontainer -t "${DEVCONTAINER_REPOSITORY}" --build-arg BUILDKIT_INLINE_CACHE=1 --cache-from "${existing_image}" "${this_dir}/.."

if  "${this_dir}/diff-container-images.sh" "${existing_image}" "${DEVCONTAINER_REPOSITORY}" > /dev/null; then
  echo "The new image is equivalent to the existing image"
  exit 0
fi

# Login to az cli if necessary

existing_account="$(az account show 2> /dev/null || true)"

if [ -z "$existing_account" ]; then
    az login
fi

az account set -s "${AZURE_SUBSCRIPTION}"

# Ensure we are logged in to the ACR resource, but avoid calling az acr login if an existing token is still valid.
token_file="${HOME}/.docker/config.json"

tokenExpiration=$([[ ! -f "${token_file}" ]] || decode_base64url "$(< "${token_file}" jq --arg registry "${DEVCONTAINER_REGISTRY}" -r '.auths[$registry].identitytoken' | cut -d "." -f 2)" | jq .exp 2> /dev/null || true)
currentTime=$(date +%s)
if (((tokenExpiration - currentTime) < 900)); then
    az acr login -n "$DEVCONTAINER_REGISTRY_SUBDOMAIN"
fi

# push the image to ACR

docker push ${DEVCONTAINER_REPOSITORY}

# Get the image digest for this repository (it is not the same as the image ID).
image_digest_reference="$(docker inspect $DEVCONTAINER_REPOSITORY | jq -r --arg repo ${DEVCONTAINER_REPOSITORY} '.[0].RepoDigests[] | select (startswith($repo))')"

# Update the devcontainer.json file's image property

escaped_image_repo="$(echo ${DEVCONTAINER_REPOSITORY} | sed 's/\//\\\//')"
image_digest_pattern="${escaped_image_repo}@sha256:[0-9a-f]*"
escaped_digest_reference="$(echo "${image_digest_reference}" | sed 's/\//\\\//')"

sed -i "s/${image_digest_pattern}/${escaped_digest_reference}/" "${this_dir}/devcontainer.json"
