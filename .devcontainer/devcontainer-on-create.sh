#!/usr/bin/env bash

# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

# This script initializes the devcontainer after it has been created.

set -euo pipefail

# Download Go and nuget packages
make -f "$(dirname "$0")/../Makefile" restore || true

make install-cli || true

make az-login-from-host &> /dev/null || true
