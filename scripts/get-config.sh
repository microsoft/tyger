#! /bin/bash

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
  --dev               Render the development config instead of the tyger config
  -e | --expression   The expression to evaluate. Defaults to '.'
  -o | --output       The output format. Defaults to 'yaml'
  -h, --help          Brings up this menu
EOF
}

dev=false
expression="."
format="yaml"

while [[ $# -gt 0 ]]; do
  key="$1"

  case $key in
  --dev)
    dev=true
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
config_dir="${TYGER_ENVIRONMENT_CONFIG_DIR:-${this_dir}/../deploy/config/microsoft}"

devconfig_path="${config_dir}/devconfig.yml"

if [[ "$dev" == true ]]; then
  config_path="$devconfig_path"
else
  config_path="${config_dir}/config.yml"

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

  export TYGER_MIN_NODE_COUNT="${TYGER_MIN_NODE_COUNT:-0}"
  export TYGER_LOCATION="${TYGER_LOCATION:-westus2}"

  repo_fqdn=$(envsubst <"${devconfig_path}" | yq ".wipContainerRegistry.fqdn")

  if [[ -n "${EXPLICIT_IMAGE_TAG:-}" ]]; then
		TYGER_SERVER_IMAGE="${repo_fqdn}/tyger-server:${EXPLICIT_IMAGE_TAG}"
		BUFFER_SIDECAR_IMAGE="${repo_fqdn}/buffer-sidecar:${EXPLICIT_IMAGE_TAG}"
		WORKER_WAITER_IMAGE="${repo_fqdn}/worker-waiter:${EXPLICIT_IMAGE_TAG}"
	else
    set +e # ignore errors if dev image is not present
		TYGER_SERVER_IMAGE="$(docker inspect "${repo_fqdn}/tyger-server:dev" | jq -r --arg repo "${repo_fqdn}/tyger-server" '.[0].RepoDigests[] | select (startswith($repo))')"
		BUFFER_SIDECAR_IMAGE="$(docker inspect "${repo_fqdn}/buffer-sidecar:dev" | jq -r --arg repo "${repo_fqdn}/buffer-sidecar" '.[0].RepoDigests[] | select (startswith($repo))')"
		WORKER_WAITER_IMAGE="$(docker inspect "${repo_fqdn}/worker-waiter:dev" | jq -r --arg repo "${repo_fqdn}/worker-waiter" '.[0].RepoDigests[] | select (startswith($repo))')"
    set -e
	fi

  export TYGER_SERVER_IMAGE
  export BUFFER_SIDECAR_IMAGE
  export WORKER_WAITER_IMAGE
fi

envsubst <"${config_path}" | yq eval -e "${expression}" -o "${format}" -
