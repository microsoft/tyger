#!/usr/bin/env bash

# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

# This script initializes the devcontainer after it has been created.

set -euo pipefail

# If this is not an Azure VM, add a firewall rule to the deployment config
# so this machine's external IP will be able to access the database.
if ! curl -s --fail -H Metadata:true --noproxy "*" "http://169.254.169.254/metadata/instance?api-version=2021-02-01" --connect-timeout 2; then
    echo "Not an Azure VM. Adding firewall rule to allow access to database."

    ip=$(curl -s --fail https://ipinfo.io/ip)

    echo "export TYGER_ENVIRONMENT_FIREWALL_RULES=\"[{ \\\"name\\\": \\\"devcontainer\\\", \\\"startIpAddress\\\": \\\"$ip\\\", \\\"endIpAddress\\\": \\\"$ip\\\"}]\"" |
        sudo tee -a /opt/devcontainer/devcontainer.bashrc >/dev/null
fi

# Download Go and nuget packages
make -f "$(dirname "$0")/../Makefile" restore || true

# make install-cli || true

make az-login-from-host &> /dev/null || true
