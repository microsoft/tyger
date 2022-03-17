#! /bin/bash

set -eu

usage()
{
  cat << EOF

A script to remove a specific cluster. Clusters that are not marked as ephemeral will not be removed
unless --force option is used.

Usage: $0 [options]

Options:
  --config,-c <config file>     The configuration file for the cluster
  --force                       Force deletion of non-ephemeral cluster
  -s|--delete-storage           Delete storage if cluster is not ephemeral (requires --force)
  -h, --help                    Brings up this menu
EOF
}

config_file=""
force=0
while [[ $# -gt 0 ]]; do
  key="$1"

  case $key in
    --config|-c)
      config_file="${2}"
      shift
      shift
      ;;
    --force|-f)
      force=1
      shift
      ;;
    --delete-storage|-s)
      delete_storage=1
      shift
      ;;
    -h|--help)
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

if [[ -z "$(az account show --query "environmentName")" ]]; then
  echo "You are not logged in. Please use 'az login'"
  exit 1
fi

# Merge the specific cluster config onto the default and validate
merged_config=$("$(dirname "$0")"/get-validated-cluster-definition.sh "$config_file")

# Make sure we are in the right subscription
az account set -s "$(echo "$merged_config" | jq -r .subscription)"

ephemeral=$(echo "${merged_config}" | jq -r .isEphemeral)
if [[ "$ephemeral" == "false" && $force -eq 0 ]]; then
  echo "Cluster isEphemeral flag is ${ephemeral}. Use --force to remove cluster"
  exit 1
fi

cluster_name=$(echo "${merged_config}" | jq -r .clusterName)
rg_name="${cluster_name}-rg"

# If this cluster has access to KV remove the access
managed_identity_object_id=$(az aks list -o json | jq -r --arg cluster "$cluster_name" '.[] | select(.name == $cluster).addonProfiles.azureKeyvaultSecretsProvider.identity.objectId')

if [[ -n "$(az keyvault show -n eminence | jq --arg id "$managed_identity_object_id" '.properties.accessPolicies[] | select(.objectId == $id)')" ]]; then
  az keyvault delete-policy -n eminence --object-id "$managed_identity_object_id"
fi

# Remove the cluster
if [[ "$(az group list --query "[?name=='$rg_name'] | length(@)")" != "0" ]]; then
  az group delete -g "$rg_name" -y > /dev/null
fi

# Remove storage
if [[ "$ephemeral" == "true" ]] || [[ -n "${delete_storage:-}" ]]; then
  storage_rg_name="${cluster_name}-storage-rg"
  if [[ "$(az group list --query "[?name=='$storage_rg_name'] | length(@)")" != "0" ]]; then
    az group delete -g "$storage_rg_name" -y > /dev/null
  fi
fi
