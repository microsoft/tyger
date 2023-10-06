#! /bin/bash

set -euo pipefail

usage() {
  cat <<EOF

Builds container images

Usage: $0 [options]

Options:
  -r, --registry                   The FQDN of container registry to push to.
  --test                           Build (and optionally push) test images, otherwise runtime images
  --helm                           Package and push the Tyger Helm chart
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
  -r | --registry)
    container_registry_fqdn="$2"
    shift 2
    ;;
  --test)
    test=1
    shift
    ;;
  --helm)
    helm=1
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

export DOCKER_BUILDKIT=1

repo_root_dir="$(dirname "$0")/.."

function build_and_push() {
  echo "Building image ${local_tag}..."
  docker build -f "${dockerfile_path}" -t "${local_tag}" --target "${target}" --build-arg TYGER_VERSION="${image_tag}" $quiet "${build_context}" >/dev/null

  if [[ -z "${push:-}" ]]; then
    return 0
  fi

  full_image="${container_registry_fqdn}/${remote_repo}:${image_tag}"

  # Push image
  if [[ -z "${force:-}" ]]; then
    # First try to pull the image
    image_exists=$("$(dirname "${0}")/docker-auth-wrapper.sh" pull "$full_image" 2>/dev/null || true)
    if [[ -n "$image_exists" ]]; then
      echo "Attempting to push an image that already exists: $full_image"
      echo "Use \"--push-force\" to overwrite existing image tags"
      exit 1
    fi
  fi

  docker tag "${local_tag}" "$full_image"
  echo "Pushing image ${full_image}..."
  "$(dirname "${0}")/docker-auth-wrapper.sh" push $quiet "$full_image" >/dev/null
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

if [[ -n "${helm:-}" ]]; then
  echo "logging in to ACR to publish helm chart..."
  token=$(az acr login --name "${container_registry_fqdn}" --expose-token --output tsv --query accessToken --only-show-errors)
  username="00000000-0000-0000-0000-000000000000"
  echo "${token}" | docker login "${container_registry_fqdn}" -u "${username}" --password-stdin

  helm_repo_namespace="oci://${container_registry_fqdn}/helm"
  chart_dir=${repo_root_dir}/deploy/helm/tyger
  package_dir=$(mktemp -d)

  if [[ -z "${force:-}" ]]; then
    # Check to see if this chart already exists in the registry
    chart_already_exists=$(helm pull "${helm_repo_namespace}/tyger" --version "${image_tag}" --destination "${package_dir}" 2>/dev/null || true)
    if [[ -n "$chart_already_exists" ]]; then
      echo "Attempting to push an helm chart that already exists: ${image_tag}"
      echo "Use \"--push-force\" to overwrite an existing chart"
      rm -rf "${package_dir}"
      exit 1
    fi
  fi

  helm package "${chart_dir}" --destination "${package_dir}" --app-version "${image_tag}" --version "${image_tag}" >/dev/null
  package_name=$(ls "${package_dir}")

  echo "Pushing helm chart..."
  helm push "${package_dir}/${package_name}" "${helm_repo_namespace}"

  rm -rf "${package_dir}"
fi
