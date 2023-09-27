#!/bin/bash

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

# Check if the command failed and the error text contains the specific string
if [[ "$error_output" =~ "unauthorized: authentication required" ]]; then
    echo "Attempting to log in..." >&2

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

    registry_name=$(echo "$image_name" | cut -d'/' -f1)
    "$(dirname "${0}")"/check-login.sh
    az acr login -n "$registry_name"

    # Run the command again
    docker "$@"
else
    echo "$error_output" >&2
fi
