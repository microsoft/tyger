#!/usr/bin/env bash

# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -eu

if [[ -z "$(az account show --query "environmentName")" ]]; then
  echo "You are not logged in. Please use 'az login'"
  exit 1
fi
