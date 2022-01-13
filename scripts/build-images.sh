#! /bin/bash

set -euo pipefail

usage() {
  cat <<EOF

Builds runtime images

Usage: $0 [options]

Options:
  --compress                       Compress the runtime binaries with upx
  --push                           Push runtime images (requires --tag or --use-git-hash-as-tag)
  --push-force                     Force runtime images, will overwrite images with same tag (requires --tag or --use-git-hash-as-tag)
  --tag <tag>                      Tag for runtime images
  --use-git-hash-as-tag            Use the current git hash as tag
  -h, --help                       Brings up this menu
EOF
}

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

while [[ $# -gt 0 ]]; do
  key="$1"

  case $key in
  --compress)
    compress=1
    shift
    ;;
  --push)
    push=1
    shift
    ;;
  --push-force)
    push=1
    force=1
    shift
    ;;
  --tag)
    image_tag="$2"
    shift 2
    ;;
  --use-git-hash-as-tag)
    image_tag="$(git rev-parse HEAD)"
    shift
    ;;
  -h | --help)
    usage
    exit
    ;;
  *)
    echo "ERROR: unknown option \"$key\""
    usage
    exit 1
    ;;
  esac
done

docker_context="$(dirname "$0")/.."

tyger_server_image_name=tyger-server

export DOCKER_BUILDKIT=1
docker build -t ${tyger_server_image_name} --target tyger-server --build-arg COMPRESS="${compress:-}" "${docker_context}"
docker build -t testrecon:test --target testrecon "${docker_context}"

if [[ -z "${push:-}" ]]; then
  exit 0
fi

if [[ -z "${image_tag:-}" ]]; then
  echo "When pushing images, you must supply a tag or explicitly use the current git commit"
  echo "See \"$0 -h\" for details"
  exit 1
fi

container_registry_name=eminence
container_registry_full_name="${container_registry_name}.azurecr.io"

# Ensure we are logged in to the ACR resource, but avoid calling az acr login if an existing token is still valid.
token_file="${HOME}/.docker/config.json"

tokenExpiration=$([[ ! -f "${token_file}" ]] || decode_base64url "$(jq <"${token_file}" --arg registry "${container_registry_full_name}" -r '.auths[$registry].identitytoken' | cut -d "." -f 2)" | jq .exp 2>/dev/null || true)
currentTime=$(date +%s)
if (((tokenExpiration - currentTime) < 900)); then
  az acr login -n "$container_registry_name"
fi

full_image_name="${container_registry_full_name}/${tyger_server_image_name}:${image_tag}"

# First try to pull the image
image_exists=$(docker pull "$full_image_name" 2>/dev/null || true)
if [[ -n "$image_exists" ]] && [[ -z "${force:-}" ]]; then
  echo "Attempting to push an image that already exists: $full_image_name"
  echo "Use \"--push-force\" to overwrite existing image tags"
  exit 1
fi

docker tag "${tyger_server_image_name}" "$full_image_name"
docker push "$full_image_name"
