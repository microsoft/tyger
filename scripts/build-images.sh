#! /bin/bash

set -euo pipefail

usage() {
  cat <<EOF

Builds container images

Usage: $0 [options]

Options:
  -c, --environment-config         The environment configuration JSON file or - to read from stdin
  --test                           Build (and optionally push) test images, otherwise runtime images
  --push                           Push runtime images (requires --tag or --use-git-hash-as-tag)
  --push-force                     Force runtime images, will overwrite images with same tag (requires --tag or --use-git-hash-as-tag)
  --tag <tag>                      Tag for runtime images
  --use-git-hash-as-tag            Use the current git hash as tag
  -q, --quiet                      Suppress verbose output
  -h, --help                       Brings up this menu
EOF
}

quiet=""

while [[ $# -gt 0 ]]; do
  key="$1"

  case $key in
  -c | --environment-config)
    config_path="$2"
    shift 2
    ;;
  --test)
    test=1
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
  -q | --quiet)
    quiet="-q"
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

if [[ -z "${config_path:-}" ]]; then
  echo "ERROR: --environment-config parameter not specified"
  exit 1
fi

environment_definition=$(cat "${config_path}")

export DOCKER_BUILDKIT=1

repo_root_dir="$(dirname "$0")/.."

container_registry_name=$(echo "${environment_definition}" | jq -r '.dependencies.containerRegistry')
container_registry_fqdn="${container_registry_name}.azurecr.io"

function build_and_push() {
  echo "Building image ${local_tag}..."
  docker build -f "${dockerfile_path}" -t "${local_tag}" --target "${target}" $quiet "${build_context}" >/dev/null

  if [[ -z "${push:-}" ]]; then
    return 0
  fi

  "$(dirname "${0}")"/login-acr-if-needed.sh "${container_registry_fqdn}"

  full_image="${container_registry_fqdn}/${remote_repo}:${image_tag}"

  # Push image
  if [[ -z "${force:-}" ]]; then
    # First try to pull the image
    image_exists=$(docker pull "$full_image" 2>/dev/null || true)
    if [[ -n "$image_exists" ]]; then
      echo "Attempting to push an image that already exists: $full_image"
      echo "Use \"--push-force\" to overwrite existing image tags"
      exit 1
    fi
  fi

  docker tag "${local_tag}" "$full_image"
  echo "Pushing image ${full_image}..."
  docker push $quiet "$full_image" >/dev/null
}

if [[ -n "${test:-}" ]]; then
  build_context="${repo_root_dir}/cli"
  dockerfile_path="${repo_root_dir}/cli/integrationtest/testconnectivity/Dockerfile"
  target="testconnectivity"
  local_tag="testconnectivity"
  remote_repo="testconnectivity"

  build_and_push
else
  build_context="${repo_root_dir}/"
  dockerfile_path="${repo_root_dir}/server/Dockerfile"
  target="runtime"
  local_tag="tyger-server"
  remote_repo="tyger-server"

  build_and_push

  build_context="${repo_root_dir}/deploy/images/worker-waiter"
  dockerfile_path="${repo_root_dir}/deploy/images/worker-waiter/Dockerfile"
  target="worker-waiter"
  local_tag="worker-waiter"
  remote_repo="worker-waiter"

  build_and_push

  build_context="${repo_root_dir}/cli"
  dockerfile_path="${repo_root_dir}/cli/Dockerfile"
  target="buffer-sidecar"
  local_tag="buffer-sidecar"
  remote_repo="buffer-sidecar"

  build_and_push
fi
