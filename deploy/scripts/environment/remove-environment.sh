#! /bin/bash

set -euo pipefail

usage() {
    cat <<EOF

A script to remove an environment. Environments that are not marked as ephemeral will not be removed
unless --force option is used.

Usage: $0 [options]

Options:
  -c, --environment-config      The environment configuration JSON file or - to read from stdin
-f, --force                     Force deletion of non-ephemeral cluster
  -s, --delete-storage          Delete storage if cluster is not ephemeral (requires --force)
  -h, --help                    Brings up this menu
EOF
}

force=0
while [[ $# -gt 0 ]]; do
    key="$1"

    case $key in
    -c | --environment-config)
        config_path="$2"
        shift 2
        ;;
    --force | -f)
        force=1
        shift
        ;;
    --delete-storage | -s)
        delete_storage=1
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

environment_definition=$(cat "${config_path}")

ephemeral=$(echo "${environment_definition}" | jq -r .isEphemeral)
if [[ "$ephemeral" == "false" && $force -eq 0 ]]; then
    echo "Cluster isEphemeral flag is ${ephemeral}. Use --force to remove the environment"
    exit 1
fi

environment_resource_group=$(echo "${environment_definition}" | jq -r '.resourceGroup')

primary_cluster_name=$(echo "${environment_definition}" | jq -r '.primaryCluster')
primary_cluster_resource=$(az aks show -n "${primary_cluster_name}" -g "${environment_resource_group}")

managed_identity_object_id=$(echo "${primary_cluster_resource}" | jq -r '.addonProfiles.azureKeyvaultSecretsProvider.identity.objectId')

# If this cluster has access to KV remove the access
if [[ -n "${managed_identity_object_id}" ]]; then
    dependencies_subscription=$(echo "${environment_definition}" | jq -r '.dependencies.subscription')
    keyvault_name=$(echo "${environment_definition}" | jq -r '.dependencies.keyVault.name')
    az keyvault set-policy -n "$keyvault_name" --secret-permissions get --object-id "$managed_identity_object_id" --subscription "${dependencies_subscription}"
fi

# Remove the non-storage resources
if [[ "$(az group list --query "[?name=='$environment_resource_group'] | length(@)")" != "0" ]]; then
    az group delete -g "$environment_resource_group" -y >/dev/null
fi

# Remove storage
if [[ "$ephemeral" == "true" ]] || [[ -n "${delete_storage:-}" ]]; then
    for organization_name in $(echo "${environment_definition}" | jq -r '.organizations | keys[]'); do
        organization=$(echo "${environment_definition}" | jq --arg name "$organization_name" '.organizations[$name]')
        organization_resource_group=$(echo "${organization}" | jq -r '.resourceGroup')

        az group delete -g "$organization_resource_group" -y >/dev/null
    done
fi
