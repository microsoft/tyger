#!/usr/bin/env bash

# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -euo pipefail

# Check if /opt/tyger exists. If if does, exit. Otherwise, create it. This will require sudo.
# The ownership should be the same as if it sudo hasn't been used.

if [ ! -d /opt/tyger ]; then
    uid=$(id -u)
    gid=$(id -g)
    sudo mkdir /opt/tyger
    sudo chown -R "$uid":"$gid" /opt/tyger
fi

if [ ! -d /tmp/tyger ]; then
    mkdir -m 777 /tmp/tyger
fi

if [ -n "${WSL_DISTRO_NAME:-}" ]; then
    minimum_docker_desktop_version="4.31.0"

    docker_version=$(docker.exe version | grep -oP 'Server: Docker Desktop \d+\.\d+(\.\d+)?')
    docker_version=$(echo "$docker_version" | grep -oP '\d+\.\d+(\.\d+)?')
    if [ "$(printf '%s\n' $minimum_docker_desktop_version "$docker_version" | sort -V | head -n1)" != $minimum_docker_desktop_version ]; then
        echo "Docker Desktop version $docker_version is not supported. Please upgrade to version $minimum_docker_desktop_version or later."
        exit 1
    fi
fi
