#! /bin/bash

set -eu

usage()
{
  cat << EOF

A script to ensure that a specific cluster is deployed accoring to the specified configuration file.

Usage: $0 [options]

Options:
  --config,-c <config file>     The configuration file for the cluster
  --verbose, -v                 Verbose output
  -h, --help                    Brings up this menu
EOF
}

config_file=""
while [[ $# -gt 0 ]]; do
  key="$1"

  case $key in
    --config|-c)
      config_file="${2}"
      shift
      shift
      ;;
    --verbose|-v)
      verbose=1
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

if [[ -n "${verbose:-}" ]]; then
  echo "Deploying cluster with configuration:"
  echo "$merged_config" | jq .
fi

# Make sure we are in the right subscription
az account set -s "$(echo "$merged_config" | jq -r .subscription)"

if [[ "$(az provider list --query "[?namespace=='Microsoft.ContainerService']" | jq '.[] | length > 0')" != "true" ]]; then
  echo "Attempting to register provider namespace. This will fail if you are not owner of subscription"
  az provider register --namespace Microsoft.ContainerService
fi

{ az extension add --name aks-preview >/dev/null; } 2>&1
{ az extension update --name aks-preview >/dev/null; } 2>&1

cluster_name=$(echo "${merged_config}" | jq -r .clusterName)
cluster_location=$(echo "${merged_config}" | jq -r .location)
rg_name="${cluster_name}-rg"
loga_name="${cluster_name}-logs"
system_node_size=$(echo "${merged_config}" | jq -r .systemNodeSize)
dns_prefix="${cluster_name}-dns"
kubernetes_version="1.23.3"

# This is the resource group where we will keep the main AKS related resources
az group create -l "$cluster_location" -n "$rg_name" > /dev/null

# Create Log Abnalytics workspace for the cluster
# The PerGB2018 is the default, but we are being explicit here so we know where to change it later
az monitor log-analytics workspace create -n "$loga_name" -g "$rg_name" --sku "PerGB2018" > /dev/null
workspace_id="$(az monitor log-analytics workspace show -n "$loga_name" -g "$rg_name" | jq -r .id)"

# Is this a create or update?
if [[ -n "$(az aks list | jq --arg cluster "$cluster_name" '.[] | select(.name == $cluster)')" ]]; then
  az aks nodepool update -n system --cluster-name "$cluster_name" -g "$rg_name" \
    --update-cluster-autoscaler \
    --min-count 1 \
    --max-count 3

  # Enable addons
  if [[ $(az aks show -n "$cluster_name" -g "$rg_name" | jq -r '.addonProfiles.omsagent.enabled') != "true" ]]; then
    az aks enable-addons -a monitoring -n "$cluster_name" -g "$rg_name" --workspace-resource-id "$workspace_id"
  fi
  if [[ $(az aks show -n "$cluster_name" -g "$rg_name" | jq -r '.addonProfiles.azureKeyvaultSecretsProvider.enabled') != "true" ]]; then
    az aks enable-addons -a azure-keyvault-secrets-provider
  fi
else
  az aks create \
    --resource-group "$rg_name" \
    --location "$cluster_location" \
    --name "$cluster_name" \
    --enable-addons monitoring,azure-keyvault-secrets-provider \
    --workspace-resource-id "$workspace_id" \
    --kubernetes-version "$kubernetes_version" \
    --enable-cluster-autoscaler \
    --node-count 1 \
    --min-count 1 \
    --max-count 3 \
    --nodepool-name system \
    --node-vm-size "$system_node_size" \
    --load-balancer-sku standard \
    --dns-name-prefix "$dns_prefix" \
    --generate-ssh-keys
fi

az aks get-credentials -n "$cluster_name" -g "$rg_name" --overwrite-existing

# Nodepools
nodepools="$(echo "$merged_config" | jq -r '.userNodePools[].name')"
for p in $nodepools; do
  pool="$(echo "$merged_config" | jq --arg pool "$p" -r '.userNodePools[] | select(.name == $pool )')"

  # Does the pool exist, if so update, otherwise create
  if [[ -n "$(az aks nodepool list --cluster-name "$cluster_name" -g "$rg_name" | jq --arg pool "$p" '.[] | select(.name == $pool)')" ]]; then
    az aks nodepool update -n "$p" --cluster-name "$cluster_name" -g "$rg_name" \
      --update-cluster-autoscaler \
      --min-count "$(echo "$pool" | jq -r .minCount)" \
      --max-count "$(echo "$pool" | jq -r .maxCount)"
  else
    vm_size="$(echo "$pool" | jq -r .vmSize)"

    #If this is a GPU node pool, we will add some additional flags
    if [[ "$vm_size" =~ "Standard_N" ]]; then
      az aks nodepool add -n "$p" --cluster-name "$cluster_name" -g "$rg_name" \
        --kubernetes-version "$kubernetes_version" \
        --enable-cluster-autoscaler \
        --node-count 1 \
        --min-count "$(echo "$pool" | jq -r .minCount)" \
        --max-count "$(echo "$pool" | jq -r .maxCount)" \
        --node-vm-size "$vm_size" \
        --labels tyger=run \
        --node-taints tyger=run:NoSchedule,sku=gpu:NoSchedule \
        --aks-custom-headers UseGPUDedicatedVHD=true
    else
      az aks nodepool add -n "$p" --cluster-name "$cluster_name" -g "$rg_name" \
        --kubernetes-version "$kubernetes_version" \
        --enable-cluster-autoscaler \
        --node-count 1 \
        --min-count "$(echo "$pool" | jq -r .minCount)" \
        --max-count "$(echo "$pool" | jq -r .maxCount)" \
        --node-vm-size "$vm_size" \
        --labels tyger=run \
        --node-taints tyger=run:NoSchedule
    fi
  fi
done

# Attach ACRs
if [[ "$(echo "$merged_config" | jq -r '.containerRegistries | length')" -gt 0 ]]; then
  for acr in $(echo "$merged_config" | jq -r '.containerRegistries[]'); do
    if [[ -n "${verbose:-}" ]]; then
      echo "Attaching container registry: $acr"
    fi
      az aks update -n "$cluster_name" -g "$rg_name" --attach-acr "$acr"
  done
fi

# Deploy storage accounts
if [[ -n "${verbose:-}" ]]; then
  echo "Setting up storage accounts"
fi
storage_rg_name="${cluster_name}-storage-rg"
buffersSa=$(echo "${merged_config}" | jq -r .storageAccounts.buffers)
storageServerSa=$(echo "${merged_config}" | jq -r .storageAccounts.storageServer)
az group create -l "$cluster_location" -n "$storage_rg_name" > /dev/null
az storage account create -n "$buffersSa" -g "$storage_rg_name" -l "$cluster_location"
az storage account create -n "$storageServerSa" -g "$storage_rg_name" -l "$cluster_location"

# Create the tyger namespace if it does not exits:
if [ -z "$(kubectl get namespaces -o json | jq --arg ns tyger '.items[] | select(.metadata.name == $ns)')" ]; then
  kubectl create namespace tyger
fi

# Create or update storage secrets
# Note the dry-run -> apply is a "trick" that allow us to update the secret if it exists; "kubectl create secret ..." fails if secret exists
kubectl create secret -n tyger generic storageserversa --from-literal=connectionString="$(az storage account show-connection-string --name "$storageServerSa" | jq -r .connectionString)" --dry-run=client -o yaml | kubectl apply -f -
kubectl create secret -n tyger generic bufferssa --from-literal=connectionString="$(az storage account show-connection-string --name "$buffersSa" | jq -r .connectionString)" --dry-run=client -o yaml | kubectl apply -f -
