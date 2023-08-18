#! /bin/bash

set -euo pipefail

usage() {
    cat <<EOF

Loads the current credentials for the logged in user so they can be used to access kubectl calls.

Usage: $0 --environment-config CONFIG_PATH

Options:
  -c | --environment-config     The environment configuration JSON file or - to read from stdin
  -h, --help                    Brings up this menu
EOF
}

while [[ $# -gt 0 ]]; do
    key="$1"

    case $key in
    -c | --environment-config)
        config_path="$2"
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

if [[ -z "${config_path:-}" ]]; then
    echo "ERROR: --environment-config parameter not specified"
    exit 1
fi

environment_definition=$(cat "${config_path}")

primary_cluster_name=$(echo "${environment_definition}" | jq -r '.primaryCluster')

context_name=$(kubectl config view -o json | jq -r --arg cluster_name "${primary_cluster_name}" '.contexts[]? | select(.context.cluster == $cluster_name and (.context.user | startswith("clusterUser"))).name')
if [[ -z "${context_name}" ]]; then
    # Ensure token is up to date if in pipeline
    "$(dirname "$0")/login-if-pipeline.sh"

    az aks get-credentials -n "${primary_cluster_name}" -g "$(echo "${environment_definition}" | jq -r '.resourceGroup')" --subscription="$(echo "${environment_definition}" | jq -r '.subscription')" --overwrite-existing
    kubelogin convert-kubeconfig -l azurecli
else
    kubectl config use-context "${context_name}"
fi
