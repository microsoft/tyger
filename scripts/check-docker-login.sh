#!/usr/bin/env bash

# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

# Checks if we need to run `az acr login` for a given registry.

set -euo pipefail

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

registry_name="$1"

if [[ -z "$registry_name" ]]; then
    echo "Please provide the registry name as the first argument"
    exit 1
fi

config_file="$HOME/.docker/config.json"
if [[ ! -f "$config_file" ]]; then
    echo "Docker config file not found at $config_file"
    exit 1
fi

jwt=$(jq --arg registry "$registry_name" -r '.auths[$registry].identitytoken // ""' "$config_file" || true)

if [[ -z "$jwt" ]]; then
    # check for credential provider
    provider=$(jq --arg registry "$registry_name" -r '.credsStore // ""' "$config_file" || true)
    if [[ -z "$provider" ]]; then
        echo "No login found for $registry_name"
        exit 1
    fi

    provider="docker-credential-$provider"
    if ! command -v "$provider" >/dev/null; then
        echo "Credential provider $provider not found"
        exit 1
    fi

    provider_response=$(echo "$registry_name" | "$provider" get)
    if [[ -z "$provider_response" ]]; then
        echo "No login found for $registry_name"
        exit 1
    fi

    jwt=$(echo "$provider_response" | jq -r '.Secret // ""' || true)
fi

if [[ -z "$jwt" ]]; then
    echo "No login found for $registry_name"
    exit 1
fi

token_expiration=$(decode_base64url "$(echo "$jwt" | cut -d "." -f 2)" | jq -r '.exp' || true)

current_time=$(date +%s)

if (((token_expiration - current_time) < 900)); then
    echo "Login for $registry_name needs to be refreshed"
    exit 1
fi
