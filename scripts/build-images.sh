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

export DOCKER_BUILDKIT=1
export HELM_EXPERIMENTAL_OCI=1

repo_root_dir="$(dirname "$0")/.."

container_registry_name=eminence
container_registry_fqdn="${container_registry_name}.azurecr.io"
tyger_server_image_short_name=tyger-server
tyger_server_full_image_name="${container_registry_fqdn}/${tyger_server_image_short_name}:${image_tag:-}"
helm_repo_namespace="oci://${container_registry_fqdn}/helm"

docker build -t ${tyger_server_image_short_name} --target ${tyger_server_image_short_name} --build-arg COMPRESS="${compress:-}" "${repo_root_dir}"
docker build -t testrecon:test --target testrecon "${repo_root_dir}"

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
