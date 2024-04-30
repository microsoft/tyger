#!/usr/bin/env bash

# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

# This script is used to copy the Azure CLI context cache directory from the host to the devcontainer.
# This can be used instead of running "az login" in the devcontainer if the user has already logged in on the host.

set -euo pipefail

if [[ -n "${WSL_DISTRO_NAME:-}" ]]; then
    echo "Reusing Azure CLI context cache is not supported from WSL." >&2
    exit 1
fi

devcontainer_image=$(docker ps --filter label=devcontainer.metadata --format json | jq -r '.Image' | head -n 1)
container_id=$(docker run -d -u "$USER" --mount "source=$(wsl_host_path "${DEVCONTAINER_HOST_HOME}/.azure/"),target=/home/${USER}/.azure,type=bind,readonly" "$devcontainer_image" 2>/dev/null || true)
if [[ -n "$container_id" ]]; then
    trap 'docker rm -f "$container_id" 2&> /dev/null' EXIT

    docker cp "$container_id:/home/${USER}/.azure/" ~/
    if az account show; then
        exit 0
    fi
fi

echo -e "\033[1;31mFailed to copy Azure CLI context cache directory from host. Please run 'az login' instead.\033[0m" >&2
exit 1
