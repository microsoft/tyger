#! /bin/bash

set -euo pipefail

usage() {
    cat <<EOF

Waits for the node count of the node pools of the primary cluster to reach at least the minimum specified in configuration or 1, whichever is smaller.

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

context_name=$(kubectl config view -o json | jq -r --arg cluster_name "${primary_cluster_name}" '.contexts | .[] | select(.context.cluster == $cluster_name).name')
if [[ -z "${context_name}" ]]; then
    az aks get-credentials -n "${primary_cluster_name}" -g "$(echo "${environment_definition}" | jq -r '.resourceGroup')" --subscription="$(echo "${environment_definition}" | jq -r '.subscription')" --overwrite-existing
else
    kubectl config use-context "${context_name}" >/dev/null
fi

primary_cluster=$(echo "${environment_definition}" | jq -r --arg pc "${primary_cluster_name}" '.clusters[$pc]')

declare -A targets
for nodepool_name in $(echo "${primary_cluster}" | jq -r '.userNodePools | keys[]'); do
    min=$(echo "${primary_cluster}" | jq -r --arg np "${nodepool_name}" '.userNodePools[$np].minCount')
    if (( min == 0 )); then
        continue
    fi

    # if min is greater than 1, set it to 1 to avoid waiting for the full scale
    if (( min > 1 )); then
        min=1
    fi

    targets["${nodepool_name}"]=${min}
done

scale_reached=false
while ! $scale_reached; do
    scale_reached=true
    for nodepool_name in "${!targets[@]}"; do
        expected_count=${targets[$nodepool_name]}

        ready_count=$(kubectl get nodes -l agentpool="${nodepool_name}" -o json | jq -r '[.items[] | select(.status.conditions[] | select(.type=="Ready" and .status == "True"))] | length')
        echo -n "${ready_count}/${expected_count} nodes ready in ${nodepool_name}. "
        if ((ready_count < expected_count)); then
            scale_reached=false
        fi
    done
    if ! $scale_reached; then
        sleep 10
        echo ""
    fi
done
