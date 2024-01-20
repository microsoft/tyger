#!/usr/bin/env bash

# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -u

# Run the command and capture its stderr, letting stdout pass through
exec 3>&1 # Create a new file descriptor and point it to stdout
error_output=$(docker "$@" 2>&1 >&3)
ret=$?
exec 3>&- # Close the new file descriptor

if [[ $ret -eq 0 ]]; then
    exit
fi

set -e

# Check if the command failed and the error text contains one of the specific strings
if [[ "$error_output" =~ "unauthorized: authentication required" || "$error_output" =~ "denied: requested access to the resource is denied" ]]; then
    echo "Logging in to ACR..." >&2
    "$(dirname "${0}")"/check-az-login.sh

    image_name=""
    counter=0

    # Loop through arguments to find the second one that doesn't start with '-'
    for arg in "$@"; do
        if [[ ! $arg == -* ]]; then
            ((counter += 1))
            if [[ $counter -eq 2 ]]; then
                image_name="$arg"
                break
            fi
        fi
    done

    # Check if image_name was found
    if [[ -z $image_name ]]; then
        echo "Image argument not found."
        exit 1
    fi

    registry_name=$(echo "$image_name" | cut -d'.' -f1)
    "$(dirname "${0}")"/check-az-login.sh

    if ! az acr login -n "$registry_name" &> /dev/null; then
        # If this script is called concurrently, the credential manager may fail, so we try again
        az acr login -n "$registry_name"
    fi

    # Run the command again
    docker "$@"
else
    echo "$error_output" >&2
    exit 1
fi
