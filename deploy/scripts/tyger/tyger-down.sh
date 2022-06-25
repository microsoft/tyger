#! /bin/bash

set -euo pipefail

usage() {
    cat <<EOF

Removes tyger instances from an environment.

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
    ak aks get-credentials -n "${primary_cluster_name}" -g "$(echo "${environment_definition}" | jq -r '.resourceGroup')"
else
    kubectl config use-context "${context_name}" >/dev/null
fi

dns_zone=$(echo "${environment_definition}" | jq -r '.dependencies.dnsZone.name')

for organization_name in $(echo "${environment_definition}" | jq -r '.organizations | keys[]'); do
    organization=$(echo "${environment_definition}" | jq --arg name "$organization_name" '.organizations[$name]')
    subdomain=$(echo "${organization}" | jq -r '.subdomain')
    namespace=$(echo "${organization}" | jq -r '.namespace')
    helm_release="tyger"

    if [[ $(helm list -n "${namespace}" -l name="${helm_release}" -o json | jq length) != 0 ]]; then
        helm delete -n "${namespace}" "${helm_release}"
    fi

    kubectl delete pvc -n "${namespace}" -l app.kubernetes.io/instance="${helm_release}"

    for pod in $(kubectl get pod -n "${namespace}" -l tyger-run -o go-template='{{range .items}}{{.metadata.name}}{{"\n"}}{{end}}'); do
        kubectl patch pod -n "${namespace}" "${pod}" \
            --type json \
            --patch='[ { "op": "remove", "path": "/metadata/finalizers" } ]'
    done

    kubectl delete job -n "${namespace}" -l tyger-run --cascade=foreground
    kubectl delete statefulset -n "${namespace}" -l tyger-run --cascade=foreground
    kubectl delete secret -n "${namespace}" -l tyger-run --cascade=foreground
    kubectl delete service -n "${namespace}" -l tyger-run --cascade=foreground

    subscription=$(echo "${environment_definition}" | jq -r '.dependencies.subscription')
    dns_zone=$(echo "${environment_definition}" | jq -r '.dependencies.dnsZone.name')
    dns_resource_group=$(echo "${environment_definition}" | jq -r '.dependencies.dnsZone.resourceGroup')

    az network dns record-set cname delete -y -g "${dns_resource_group}" -z "${dns_zone}"  -n "${subdomain}" --subscription "${subscription}"
done
