#! /bin/bash

set -euo pipefail

usage() {
    cat <<EOF

Deploys tyger instances to all organizations in an environment.

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
    ak aks get-credentials -n "${primary_cluster_name}" -g "$(echo "${environment_definition}" | jq -r '.resourceGroup')" --subscription="$(echo "${environment_definition}" | jq -r '.subscription')" --overwrite-existing
else
    kubectl config use-context "${context_name}" >/dev/null
fi

tyger_server_image="$(docker inspect eminence.azurecr.io/tyger-server:dev | jq -r --arg repo eminence.azurecr.io/tyger-server '.[0].RepoDigests[] | select (startswith($repo))')"
tyger_chart_location="$(dirname "$0")/../helm/tyger"

dns_zone=$(echo "${environment_definition}" | jq -r '.dependencies.dnsZone.name')

for organization_name in $(echo "${environment_definition}" | jq -r '.organizations | keys[]'); do
    organization=$(echo "${environment_definition}" | jq --arg name "$organization_name" '.organizations[$name]')
    subdomain=$(echo "${organization}" | jq -r '.subdomain')
    namespace=$(echo "${organization}" | jq -r '.namespace')
    hostname="${subdomain}.${dns_zone}"
    helm_release="tyger"
    cluster_config=$(echo "${environment_definition}" | jq -c '.clusters')

    # TODO: note that more than one buffer storage account is not currently implemented.

    values=$(
        cat <<- END
server:
    image: "${tyger_server_image}"
    hostname: "${hostname}"
    security:
        enabled: true
        authority: "$(echo "${organization}" | jq -r '.authority')"
        audience: "$(echo "${organization}" | jq -r '.audience')"
    storageAccountConnectionStringSecretName: "$(echo "${organization}" | jq -r '.storage.buffers[0].name')"
    clusterConfigurationJson: |
        ${cluster_config}

storageServer:
    storageAccountConnectionStringSecretName: $(echo "${organization}" | jq -r '.storage.storageServer.name')
END
    )

    echo
    echo "Installing Helm chart..."
    helm_version=$(echo "${values}" \
        | helm upgrade --install \
            --create-namespace -n "${namespace}" \
            "${helm_release}" "${tyger_chart_location}" \
            --atomic --output json -f - \
        | jq -r '.version')

    if [[ "${helm_version}" == "1" ]]; then
        subscription=$(echo "${environment_definition}" | jq -r '.dependencies.subscription')
        dns_resource_group=$(echo "${environment_definition}" | jq -r '.dependencies.dnsZone.resourceGroup')

        # Figure out DNS name for CNAME
        public_ip=$(kubectl get -n traefik svc -o json | jq -r .items[0].status.loadBalancer.ingress[0].ip)
        fqdn=$(az network public-ip list --subscription "${subscription}" -o json | jq -r --arg ip "$public_ip" '.[] | select(.ipAddress == $ip) | .dnsSettings.fqdn')

        # Modify the zone
        az network dns record-set cname create -g "${dns_resource_group}" -z "${dns_zone}" -n "${subdomain}" --subscription "${subscription}"
        az network dns record-set cname set-record -g "${dns_resource_group}" -z "${dns_zone}" -n "${subdomain}" -c "$fqdn" --subscription "${subscription}"
    fi

    health_check_endpoint="https://${hostname}/healthcheck"
    echo "Waiting for successful health check at ${health_check_endpoint}"
    for wait in {0..30}; do
        if [[ "$(curl -s -o /dev/null -m 1 -w '%{http_code}' "$health_check_endpoint")" == "200" ]]; then
            echo "Ready"
            continue 2
        fi
        echo "Waiting...$wait"
        sleep 1
    done

    # We should not get here if service is healthy
    exit 1

done
