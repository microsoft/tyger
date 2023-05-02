#!/bin/bash
#
# Run this script when you make changes to the "devcontainer" target of the Dockerfile in the root of this repo.
# It builds the image, pushes it a container registry, and updates the "image" property in the devcontainer.json field.

set -euo pipefail

azure_subscription="BiomedicalImaging-NonProd"
devcontainer_registry_subdomain="compimagdevcontainers"
devcontainer_registry="${devcontainer_registry_subdomain}.azurecr.io"
devcontainer_repository="${devcontainer_registry}/tyger"

# Build the image

this_dir=$(dirname "$0")
existing_image=$("${this_dir}/get-devcontainer-image.sh")

export DOCKER_BUILDKIT=1

docker build -f "${this_dir}/Dockerfile" --target devcontainer -t "${devcontainer_repository}" --build-arg BUILDKIT_INLINE_CACHE=1 --cache-from "${existing_image}" "${this_dir}/.."

if  "${this_dir}/diff-container-images.sh" "${existing_image}" "${devcontainer_repository}" > /dev/null; then
  echo "The new image is equivalent to the existing image"
  exit 0
fi

# Login to az cli if necessary

existing_account="$(az account show 2> /dev/null || true)"

if [ -z "$existing_account" ]; then
    az login
fi

az account set -s "${azure_subscription}"

"${this_dir}"/../scripts/login-acr-if-needed.sh "${devcontainer_registry_subdomain}"

# push the image to ACR

docker push ${devcontainer_repository}

# Get the image digest for this repository (it is not the same as the image ID).
image_digest_reference="$(docker inspect $devcontainer_repository | jq -r --arg repo ${devcontainer_repository} '.[0].RepoDigests[] | select (startswith($repo))')"

# Update the devcontainer.json file's image property

escaped_image_repo="$(echo ${devcontainer_repository} | sed 's/\//\\\//')"
image_digest_pattern="${escaped_image_repo}@sha256:[0-9a-f]*"
escaped_digest_reference="$(echo "${image_digest_reference}" | sed 's/\//\\\//')"

sed -i "s/${image_digest_pattern}/${escaped_digest_reference}/" "${this_dir}/devcontainer.json" "${this_dir}/../azure-pipelines.yml" "${this_dir}/../azure-pipelines-delete-environment.yml"
