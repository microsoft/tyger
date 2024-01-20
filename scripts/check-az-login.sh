#!/usr/bin/env bash

# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -u

if [[ -z "$(az account show --query "environmentName" 2> /dev/null || true)" ]]; then
  echo -e "\033[1;31mYou are not logged in to Azure. Please run 'az login'.\033[0m" >&2
  if [[ -n "${DEVCONTAINER_HOST_HOME:-}" ]]; then
    echo -e "\033[1;31mAlternatively, if you have logged in on the machine hosting the devcontainer, you can try to run 'make az-login-from-host'\033[0m" >&2
  fi
  exit 1
fi
