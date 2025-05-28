#!/usr/bin/env bash

# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -euo pipefail

usage() {
  cat <<EOF

Renders either the tyger environment configuration or the developer configuration. The environment name and configuration directory
can be overridden by setting the TYGER_ENVIRONMENT_NAME and TYGER_ENVIRONMENT_CONFIG_DIR environment variables respectively.
The default environment name is your git alias and the default config dir is <repo_root>/deploy/config/dev.

Other environment variables that can be set to change the output are:
  - TYGER_MIN_NODE_COUNT
  - TYGER_LOCATION
  - TYGER_HELM_CHART_DIR

Usage: $0 [--dev] [-e|--expression expression]

Options:
  --dev                    Render the development config instead of the tyger config
  --docker                 Render the docker tyger config instead of the cloud config
  -e | --expression        The expression to evaluate. Defaults to '.'
  -o | --output            The output format. Defaults to 'yaml'
  --pretty-print-template  Pretty-print the template file instead of rendering the config
  -h, --help               Brings up this menu
EOF
}

dev=false
docker=false
expression="."
format="yaml"
pretty_print_template=false

while [[ $# -gt 0 ]]; do
  key="$1"

  case $key in
  --dev)
    dev=true
    shift
    ;;
  --docker)
    docker=true
    shift
    ;;
  -e | --expression)
    expression="$2"
    shift 2
    ;;
  -o | --output)
    format="$2"
    shift 2
    ;;
  --pretty-print-template)
    pretty_print_template=true
    shift
    ;;
  -h | --help)
    usage
    exit
    ;;
  *)
    echo "ERROR: unknown option \"$key\""
    usage
    exit 1
    ;;
  esac
done

this_dir=$(dirname "${0}")
config_dir=$(realpath "${TYGER_ENVIRONMENT_CONFIG_DIR:-${this_dir}/../deploy/config/microsoft}")

devconfig_path="${config_dir}/devconfig.yml"

if [[ "$dev" == true ]]; then
  config_path="$devconfig_path"
else
  if [[ "$docker" == true ]]; then
    config_path="${config_dir}/dockerconfig.yml"

    TYGER_INSTALLATION_PATH="$(realpath "${this_dir}/../install/local")"
    export TYGER_INSTALLATION_PATH
  else
    config_path="${config_dir}/cloudconfig.yml"

    environment_name="${TYGER_ENVIRONMENT_NAME:-}"
    if [[ -z "${environment_name:-}" ]]; then
      if [[ ! "$(git config user.email)" =~ [^@]+ ]]; then
        echo >&2 "Set the TYGER_ENVIRONMENT_NAME environment variable or ensure your git email is set"
        exit 1
      fi
      environment_name="${BASH_REMATCH[0]//[.\-_]/}"
    fi
    export TYGER_ENVIRONMENT_NAME="${environment_name}"
    export TYGER_ENVIRONMENT_NAME_NO_DASHES="${environment_name//-/}"

    TYGER_HELM_CHART_DIR=$(readlink -f "${this_dir}/../deploy/helm/tyger")
    export TYGER_HELM_CHART_DIR

    export TYGER_MIN_CPU_NODE_COUNT="${TYGER_MIN_CPU_NODE_COUNT:-${TYGER_MIN_NODE_COUNT:-0}}"
    export TYGER_MIN_GPU_NODE_COUNT="${TYGER_MIN_GPU_NODE_COUNT:-${TYGER_MIN_NODE_COUNT:-0}}"
    export TYGER_DATABASE_LOCATION="${TYGER_DATABASE_LOCATION:-${TYGER_LOCATION:-westus3}}"
    export TYGER_LOCATION="${TYGER_LOCATION:-westus2}"

    if [[ "$TYGER_LOCATION" == "eastus" ]]; then
      export TYGER_SYSTEM_NODE_SKU="${TYGER_SYSTEM_NODE_SKU:-Standard_e2s_v3}"
      export TYGER_CPU_NODE_SKU="${TYGER_CPU_NODE_SKU:-Standard_E8s_v3}"
      export TYGER_GPU_NODE_SKU="${TYGER_GPU_NODE_SKU:-Standard_NC6s_v3}"

      export TYGER_SECONDARY_LOCATION="${TYGER_SECONDARY_LOCATION:-westus2}"
    else
      export TYGER_SYSTEM_NODE_SKU="${TYGER_SYSTEM_NODE_SKU:-Standard_DS2_v2}"
      export TYGER_CPU_NODE_SKU="${TYGER_CPU_NODE_SKU:-Standard_DS12_v2}"
      export TYGER_GPU_NODE_SKU="${TYGER_GPU_NODE_SKU:-Standard_NC6s_v3}"

      export TYGER_SECONDARY_LOCATION="${TYGER_SECONDARY_LOCATION:-eastus}"
    fi

  fi

  repo_fqdn=$(envsubst <"${devconfig_path}" | yq ".wipContainerRegistry.fqdn")
  if [[ -n "${EXPLICIT_IMAGE_TAG:-}" ]]; then
    TYGER_SERVER_IMAGE="${repo_fqdn}/tyger-server:${EXPLICIT_IMAGE_TAG}"
    TYGER_DATA_PLANE_SERVER_IMAGE="${repo_fqdn}/tyger-data-plane-server:${EXPLICIT_IMAGE_TAG}"
    BUFFER_SIDECAR_IMAGE="${repo_fqdn}/buffer-sidecar:${EXPLICIT_IMAGE_TAG}"
    BUFFER_COPIER_IMAGE="${repo_fqdn}/buffer-copier:${EXPLICIT_IMAGE_TAG}"
    WORKER_WAITER_IMAGE="${repo_fqdn}/worker-waiter:${EXPLICIT_IMAGE_TAG}"
  elif [[ "$docker" == true ]]; then
    arch=$(dpkg --print-architecture)
    TYGER_SERVER_IMAGE=$(docker inspect "${repo_fqdn}/tyger-server:dev-${arch}" 2>/dev/null | jq -r '.[0].Id' 2>/dev/null || true)
    BUFFER_SIDECAR_IMAGE=$(docker inspect "${repo_fqdn}/buffer-sidecar:dev-${arch}" 2>/dev/null | jq -r '.[0].Id' 2>/dev/null || true)
    TYGER_DATA_PLANE_SERVER_IMAGE=$(docker inspect "${repo_fqdn}/tyger-data-plane-server:dev-${arch}" 2>/dev/null | jq -r '.[0].Id' 2>/dev/null || true)
    GATEWAY_IMAGE=$(docker inspect "${repo_fqdn}/tyger-cli:dev-${arch}" 2>/dev/null | jq -r '.[0].Id' 2>/dev/null || true)
  else
    arch="amd64"

    TYGER_SERVER_IMAGE="$(docker inspect "${repo_fqdn}/tyger-server:dev-${arch}" 2>/dev/null | jq -r --arg repo "${repo_fqdn}/tyger-server" '.[0].RepoDigests[] | select (startswith($repo))' 2>/dev/null || true)"
    BUFFER_SIDECAR_IMAGE="$(docker inspect "${repo_fqdn}/buffer-sidecar:dev-${arch}" 2>/dev/null | jq -r --arg repo "${repo_fqdn}/buffer-sidecar" '.[0].RepoDigests[] | select (startswith($repo))' 2>/dev/null || true)"
    BUFFER_COPIER_IMAGE="$(docker inspect "${repo_fqdn}/buffer-copier:dev-${arch}" 2>/dev/null | jq -r --arg repo "${repo_fqdn}/buffer-copier" '.[0].RepoDigests[] | select (startswith($repo))' 2>/dev/null || true)"
    WORKER_WAITER_IMAGE="$(docker inspect "${repo_fqdn}/worker-waiter:dev-${arch}" 2>/dev/null | jq -r --arg repo "${repo_fqdn}/worker-waiter" '.[0].RepoDigests[] | select (startswith($repo))' 2>/dev/null || true)"
  fi

  export TYGER_SERVER_IMAGE
  export BUFFER_SIDECAR_IMAGE

  if [[ "$docker" == true ]]; then
    export TYGER_DATA_PLANE_SERVER_IMAGE
    export GATEWAY_IMAGE
  else
    export WORKER_WAITER_IMAGE
    export BUFFER_COPIER_IMAGE
  fi
fi

if [[ "$pretty_print_template" == true ]]; then
  # set environment variables referenced in the templates that could be empty so that the output is deterministic
  export TYGER_ENVIRONMENT_FIREWALL_RULES='[{ "name": "dummy", "startIpAddress": "0.0.0.0", "endIpAddress": "0.0.0.0"}]'
  export BUFFER_COPIER_IMAGE="dummy"
  export BUFFER_SIDECAR_IMAGE="dummy"
  export TYGER_SERVER_IMAGE="dummy"
  export WORKER_WAITER_IMAGE="dummy"
  export GATEWAY_IMAGE="dummy"
  export TYGER_DATA_PLANE_SERVER_IMAGE="dummy"
  tyger config pretty-print -i <(envsubst <"${config_path}") -o "${config_path}" --template "${config_path}"
  exit
fi

envsubst <"${config_path}" | yq eval -e "${expression}" -o "${format}" -
