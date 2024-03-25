#!/usr/bin/env bash

# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -euo pipefail

usage() {
  cat <<EOF

Builds container images

Usage: $0 [options]

Options:
  -r, --registry                   The FQDN of container registry to push to.
  --test-connectivity              Build (and optionally push) the testconnectivity image
  --tyger-server     Build (and optionally push) the tyger-server image
  --worker-waiter                  Build (and optionally push) the worker-waiter image
  --buffer-sidecar                 Build (and optionally push) the buffer-sidecar image
  --helm                           Package and push the Tyger Helm chart
  --push                           Push runtime images (requires --tag or --use-git-hash-as-tag)
  --push-force                     Force runtime images, will overwrite images with same tag (requires --tag or --use-git-hash-as-tag)
  --tag <tag>                      Tag for runtime images
  --use-git-hash-as-tag            Use the current git hash as tag
  -h, --help                       Brings up this menu
EOF
}

while [[ $# -gt 0 ]]; do
  key="$1"

  case $key in
  -r | --registry)
    container_registry_fqdn="$2"
    shift 2
    ;;
  --test-connectivity)
    test_connectivity=1
    shift
    ;;
  --tyger-server)
    tyger_server=1
    shift
    ;;
  --worker-waiter)
    worker_waiter=1
    shift
    ;;
  --buffer-sidecar)
    buffer_sidecar=1
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

function build_and_push_platform() {
  full_image="${container_registry_fqdn}/${repo}:${image_tag_with_platform}"
  echo "Building image ${full_image}..."

  set +e
  output=$(docker buildx build --platform "${platform}" -f "${dockerfile_path}" -t "${full_image}" --target "${target}" --build-arg TYGER_VERSION="${image_tag}" "${build_context}" --provenance false --progress plain 2>&1)
  ret=$?
  set -e
  if [[ $ret -ne 0 ]]; then
    echo "$output"
    exit 1
  fi

  if [[ -z "${push:-}" ]]; then
    return 0
  fi

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

  echo "Pushing image ${full_image}..."
  "$(dirname "${0}")/docker-auth-wrapper.sh" push --quiet "$full_image" >/dev/null
}

function build_and_push() {
  platform=amd64
  image_tag_with_platform=$"${image_tag}-${platform}"
  build_and_push_platform

  platform=arm64
  image_tag_with_platform=$"${image_tag}-${platform}"
  build_and_push_platform

  if [[ -z "${push:-}" ]]; then
    return 0
  fi

  manifest_name="${container_registry_fqdn}/${repo}:${image_tag}"
  docker manifest create --amend "${manifest_name}" "${container_registry_fqdn}/${repo}:${image_tag}-amd64" "${container_registry_fqdn}/${repo}:${image_tag}-arm64" > /dev/null

  # Push manigest
  if [[ -z "${force:-}" ]]; then
    # First try to pull the image
    manifest_exists=$("$(dirname "${0}")/docker-auth-wrapper.sh" pull "$manifest_name" 2>/dev/null || true)
    if [[ -n "$manifest_exists" ]]; then
      echo "Attempting to push a manifest that already exists: $manifest_name"
      echo "Use \"--push-force\" to overwrite existing image tags"
      exit 1
    fi
  fi

  docker manifest push "${manifest_name}" --purge > /dev/null
  docker tag "${container_registry_fqdn}/${repo}:${image_tag}-$(dpkg --print-architecture)" "$manifest_name"
}

if [[ -n "${test_connectivity:-}" ]]; then
  build_context="${repo_root_dir}/cli"
  dockerfile_path="${repo_root_dir}/cli/integrationtest/testconnectivity/Dockerfile"
  target="testconnectivity"
  repo="testconnectivity"

  build_and_push
fi

if [[ -n "${tyger_server:-}" ]]; then
  build_context="${repo_root_dir}/"
  dockerfile_path="${repo_root_dir}/server/Dockerfile"
  target="control-plane"
  repo="tyger-server"

  build_and_push

  target="data-plane"
  repo="tyger-data-plane-server"

  build_and_push
fi

if [[ -n "${worker_waiter:-}" ]]; then
  build_context="${repo_root_dir}/deploy/images/worker-waiter"
  dockerfile_path="${repo_root_dir}/deploy/images/worker-waiter/Dockerfile"
  target="worker-waiter"
  repo="worker-waiter"

  build_and_push
fi

if [[ -n "${buffer_sidecar:-}" ]]; then
  build_context="${repo_root_dir}/cli"
  dockerfile_path="${repo_root_dir}/cli/Dockerfile"
  target="buffer-sidecar"
  repo="buffer-sidecar"

  build_and_push

  target="docker-gateway"
  repo="docker-gateway"

  build_and_push
fi

if [[ -n "${helm:-}" ]]; then
  echo "logging in to ACR to publish helm chart..."
  "$(dirname "${0}")/check-az-login.sh"

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
