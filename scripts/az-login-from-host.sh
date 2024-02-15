#!/usr/bin/env bash

# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

# This script is used to copy the Azure CLI context cache directory from the host to the devcontainer.
# This can be used instead of running "az login" in the devcontainer if the user has already logged in on the host.

set -euo pipefail

devcontainer_id=$(head -1 /proc/self/cgroup|cut -d/ -f3)
devcontainer_image=$(docker inspect "$devcontainer_id" | jq -r '.[0].Image')
container_id=$(docker run -d -u "$USER" --mount "source=${DEVCONTAINER_HOST_HOME}/.azure/,target=/home/${USER}/.azure,type=bind,readonly" "$devcontainer_image" 2>/dev/null || true)
if [[ -n "$container_id" ]]; then
    trap 'docker rm -f "$container_id" 2&> /dev/null' EXIT

    docker cp "$container_id:/home/${USER}/.azure/" ~/
    if az account show; then
        exit 0
    fi
fi

echo -e "\033[1;31mFailed to copy Azure CLI context cache directory from host. Please run 'az login' instead.\033[0m" >&2
exit 1
