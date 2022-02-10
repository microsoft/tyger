#! /bin/bash

set -euo pipefail

usage() {
  cat <<EOF

Builds runtime images

Usage: $0 [options]

Options:
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
export HELM_EXPERIMENTAL_OCI=1

repo_root_dir="$(dirname "$0")/.."

container_registry_name=eminence
container_registry_fqdn="${container_registry_name}.azurecr.io"
tyger_server_image_short_name=tyger-server
tyger_server_full_image_name="${container_registry_fqdn}/${tyger_server_image_short_name}:${image_tag:-}"
helm_repo_namespace="oci://${container_registry_fqdn}/helm"

docker build -f "${repo_root_dir}/server/Dockerfile" -t ${tyger_server_image_short_name} "${repo_root_dir}/server"
docker build -f "${repo_root_dir}/cli/test/testrecon/Dockerfile" -t testrecon:test --target testrecon "${repo_root_dir}/cli"

if [[ -z "${push:-}" ]]; then
  exit 0
fi

if [[ -z "${image_tag:-}" ]]; then
  echo "When pushing images, you must supply a tag or explicitly use the current git commit"
  echo "See \"$0 -h\" for details"
  exit 1
fi

# Ensure we are logged in to the ACR resource, but avoid calling az acr login if an existing token is still valid.
token_file="${HOME}/.docker/config.json"
username="00000000-0000-0000-0000-000000000000"
token=$([[ ! -f "${token_file}" ]] || jq <"${token_file}" --arg registry "${container_registry_fqdn}" -r '.auths[$registry].identitytoken' 2>/dev/null || true)
tokenExpiration=$([[ -z "${token}" ]] || decode_base64url "$(echo "${token}" | cut -d "." -f 2)" | jq .exp 2>/dev/null || true)
currentTime=$(date +%s)
if (((tokenExpiration - currentTime) < 900)); then
  echo "logging in to acr.."
  token=$(az acr login --name "${container_registry_name}" --expose-token --output tsv --query accessToken --only-show-errors)
  echo "${token}" | docker login ${container_registry_fqdn} -u "${username}" --password-stdin
fi

############################
# Pushing tyger server image
############################
if [[ -z "${force:-}" ]]; then
  # First try to pull the image
  image_exists=$(docker pull "$tyger_server_full_image_name" 2>/dev/null || true)
  if [[ -n "$image_exists" ]]; then
    echo "Attempting to push an image that already exists: $tyger_server_full_image_name"
    echo "Use \"--push-force\" to overwrite existing image tags"
    exit 1
  fi
fi

docker tag "${tyger_server_image_short_name}" "$tyger_server_full_image_name"
docker push "$tyger_server_full_image_name"

############################
# Pushing tyger helm chart
############################
chart_dir=${repo_root_dir}/deploy/helm/tyger
chart_version=$(yq e '.version' "${chart_dir}/Chart.yaml")
updated_chart_version="${chart_version}-${image_tag}"
package_dir=$(mktemp -d)

echo "${token}" | helm registry login ${container_registry_fqdn} --username "${username}" --password-stdin

if [[ -z "${force:-}" ]]; then
  # Check to see if this chart already exists in the registry
  chart_already_exists=$(helm pull ${helm_repo_namespace}/tyger --version "${updated_chart_version}" --destination "${package_dir}" 2>/dev/null || true)
  if [[ -n "$chart_already_exists" ]]; then
    echo "Attempting to push an helm chart that already exists that already exists: ${updated_chart_version}"
    echo "Use \"--push-force\" to overwrite an existing chart"
    rm -rf "${package_dir}"
    exit 1
  fi
fi

helm package "${chart_dir}" --destination "${package_dir}" --app-version "${image_tag}" --version "${updated_chart_version}" > /dev/null
package_name=$(ls "${package_dir}")

helm push "${package_dir}/${package_name}" ${helm_repo_namespace}

rm -rf "${package_dir}"

############################
# Pushing tyger CLI
############################
tyger_cli_full_artifacts_name="${container_registry_fqdn}/app/tyger:${image_tag:-}"
oci_media_type="application/vnd.unknown.layer.v1+bin"

if [[ -z "${force:-}" ]]; then
  # Check to see if this chart already exists in the registry
  cli_artifact_exists=$(oras pull "$tyger_cli_full_artifacts_name" --media-type "$oci_media_type" 2>/dev/null || true)
  if [[ -n "$cli_artifact_exists" ]]; then
    echo "Attempting to push an OCI tyger CLI that already exists that already exists: ${tyger_cli_full_artifacts_name}"
    echo "Use \"--push-force\" to overwrite an existing artifact"
    exit 1
  fi
fi

# Build the executable and push it
# Note: using an absolute path fails with oras so we copy local and push
make -f "$(dirname "$0")/../Makefile" install-cli
cp "$(which tyger)" "./tyger"
oras push  "$tyger_cli_full_artifacts_name" --manifest-config /dev/null:application/vnd.unknown.config.v1+json "./tyger:${oci_media_type}"
rm -rf "./tyger"
