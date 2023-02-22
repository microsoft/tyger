#! /bin/bash

set -euo pipefail

usage() {
  cat <<EOF

Deploys infrastructure required for an environment.

Stores a hash of the environment definition and the scripts in this directory as a tag
on the environment' resource group to exit early if no changes have been detected.

Usage: $0 --environment-config CONFIG_PATH

Options:
  -c | --environment-config     The environment configuration JSON file or - to read from stdin
  -f | --force                  Always deploy, even if the envionment has not changed.
  --fail-if-not-provisioned     Quickly check if the environment is already provisioned and exit with a non-zero code if it isn't
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
  -f | --force)
    force=1
    shift
    ;;
  --fail-if-not-provisioned)
    fail_if_not_provisioned=1
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

if [[ -z "${config_path:-}" ]]; then
  echo "ERROR: --environment-config parameter not specified"
  exit 1
fi

environment_definition=$(cat "${config_path}")

subscription=$(echo "${environment_definition}" | jq -r '.subscription')
{
  read -r previous_subscription_name
  read -r subscription_id
} < <(az account show --query [name,id] -o tsv)

if [[ "${subscription}" != "${previous_subscription_name}" ]]; then
  az account set --subscription "${subscription}"
  subscription_id=$(az account show --query id -o tsv)
fi

cd "$(dirname "$0")"
script_hashes=$(find . -type f -print0 | sort -z | xargs -0 sha256sum)
environment_definition_hash=$(echo "${environment_definition}" | jq -c | sha256sum)
environment_hash=$(echo -e "${script_hashes}\n${environment_definition_hash}" | sha256sum | cut -d ' ' -f1)

environment_resource_group=$(echo "${environment_definition}" | jq -r '.resourceGroup')

environment_resource_group_id="/subscriptions/${subscription_id}/resourcegroups/${environment_resource_group}"
hash_tag=$( (az tag list --resource-id "${environment_resource_group_id}" 2>/dev/null || echo "{}") | jq -r '.properties.tags.tygerdefinitionhash')
if [[ -z "${force:-}" ]] && [[ "${hash_tag}" == "${environment_hash}" ]]; then
  echo "The environment appears to be up-to-date"
  exit 0
fi

if [[ -n "${fail_if_not_provisioned:-}" ]]; then
  echo "The environment was not provisioned"
  exit 1
fi

az tag delete --resource-id "${environment_resource_group_id}" --name tygerdefinitionhash --yes 2>/dev/null || true

environment_region=$(echo "${environment_definition}" | jq -r '.defaultRegion')
acr=$(echo "${environment_definition}" | jq -r '.dependencies.containerRegistry')

az group create -l "${environment_region}" -n "${environment_resource_group}" >/dev/null

if [[ "$(az provider show -n Microsoft.ContainerService | jq -r '.registrationState')" != "Registered" ]]; then
  echo "Attempting to register provider namespace. This will fail if you are not owner of subscription"
  az provider register --namespace Microsoft.ContainerService
fi

log_analytics_name=$(echo "${environment_definition}" | jq -r '.dependencies.logAnalytics.name')
log_analytics_resource_group=$(echo "${environment_definition}" | jq -r '.dependencies.logAnalytics.resourceGroup')
workspace_id="$(az monitor log-analytics workspace show -n "$log_analytics_name" -g "$log_analytics_resource_group" | jq -r .id)"

for cluster_name in $(echo "${environment_definition}" | jq -r '.clusters | keys[]'); do
  echo "Processing cluster $cluster_name..."

  cluster=$(echo "${environment_definition}" | jq --arg name "$cluster_name" '.clusters[$name]')
  aks_cluster=$(az aks show -n "$cluster_name" -g "$environment_resource_group" 2>/dev/null || true)
  cluster_region=$(echo "$cluster" | jq -r '.region')
  system_node_size=$(echo "$cluster" | jq -r '.systemNodeSize')
  dns_prefix="${cluster_name}-dns"
  kubernetes_version="1.24.6"

  # Is this a create or update?
  if [[ -z "$aks_cluster" ]]; then
    echo "Creating cluster..."
    aks_cluster=$(az aks create \
      --resource-group "$environment_resource_group" \
      --location "$cluster_region" \
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
      --generate-ssh-keys)
  fi

  if ! az aks check-acr --acr "$acr" --name "$cluster_name" -g "$environment_resource_group" 2>/dev/null | grep "cluster can pull images from"; then
    # Attach ACR
    echo "Attaching container registry: $acr"
    az aks update -n "$cluster_name" -g "$environment_resource_group" --attach-acr "$acr" -o none
  fi

  # Nodepools
  for pool_name in $(echo "${cluster}" | jq -r '.userNodePools | keys[]'); do
    pool=$(echo "${cluster}" | jq --arg name "$pool_name" '.userNodePools[$name]')
    min_count="$(echo "$pool" | jq -r .minCount)"
    max_count="$(echo "$pool" | jq -r .maxCount)"

    # Does the pool exist, if so update, otherwise create
    existing_pool=$(echo "$aks_cluster" | jq --arg pool "$pool_name" '.agentPoolProfiles[] | select(.name == $pool)')
    if [[ -n "$existing_pool" ]]; then
      if [[ "${min_count}" == "$(echo "$existing_pool" | jq -r .minCount)" ]] && [[ "${max_count}" == "$(echo "$existing_pool" | jq -r .maxCount)" ]]; then
        echo "Node pool $pool_name is up-to-date"
        continue
      else
        echo "Updating node pool $pool_name"
        az aks nodepool update -n "$pool_name" --cluster-name "$cluster_name" -g "$environment_resource_group" \
          --update-cluster-autoscaler \
          --min-count "${min_count}" \
          --max-count "${max_count}" \
          -o none
      fi
    else
      echo "Creating node pool $pool_name"

      vm_size="$(echo "$pool" | jq -r .vmSize)"

      # If this is a GPU node pool, we will add some additional flags
      if [[ "${vm_size}" =~ "Standard_N" ]]; then
        az aks nodepool add -n "$pool_name" --cluster-name "$cluster_name" -g "$environment_resource_group" \
          --kubernetes-version "$kubernetes_version" \
          --enable-cluster-autoscaler \
          --node-count "${min_count}" \
          --min-count "${min_count}" \
          --max-count "${max_count}" \
          --node-vm-size "${vm_size}" \
          --labels tyger=run \
          --node-taints tyger=run:NoSchedule,sku=gpu:NoSchedule \
          --aks-custom-headers UseGPUDedicatedVHD=true \
          -o none
      else
        az aks nodepool add -n "$pool_name" --cluster-name "$cluster_name" -g "$environment_resource_group" \
          --kubernetes-version "$kubernetes_version" \
          --enable-cluster-autoscaler \
          --node-count "${min_count}" \
          --min-count "${min_count}" \
          --max-count "${max_count}" \
          --node-vm-size "${vm_size}" \
          --labels tyger=run \
          --node-taints tyger=run:NoSchedule \
          -o none
      fi
    fi
  done
done

primary_cluster_name=$(echo "${environment_definition}" | jq -r '.primaryCluster')
primary_cluster_resource=$(az aks show -n "${primary_cluster_name}" -g "${environment_resource_group}")
az aks get-credentials -n "${primary_cluster_name}" -g "${environment_resource_group}" --overwrite-existing

####
# Set up CSI to get secrets from KeyVault
####

managed_identity_principal_id=$(echo "${primary_cluster_resource}" | jq -r '.addonProfiles.azureKeyvaultSecretsProvider.identity.clientId')
managed_identity_object_id=$(echo "${primary_cluster_resource}" | jq -r '.addonProfiles.azureKeyvaultSecretsProvider.identity.objectId')
tenant_id=$(echo "${primary_cluster_resource}" | jq -r '.identity.tenantId')

dependencies_subscription=$(echo "${environment_definition}" | jq -r '.dependencies.subscription')
keyvault_name=$(echo "${environment_definition}" | jq -r '.dependencies.keyVault.name')
tls_certificate_name=$(echo "${environment_definition}" | jq -r '.dependencies.keyVault.tlsCertificateName')

kubernetes_tls_secret_name="tyger-tls"

# We have to use object-id here because the principal id (--spn argument) fails in devops because the SP there does not have graph access
az keyvault set-policy -n "$keyvault_name" --secret-permissions get --object-id "$managed_identity_object_id" --subscription "${dependencies_subscription}" -o none

###
# Set up traefik
###

# Create the namespace if it does not exits:
if [ -z "$(kubectl get namespaces -o json | jq --arg ns traefik '.items[] | select(.metadata.name == $ns)')" ]; then
  kubectl create namespace traefik
fi

# Set up the SecretProviderClass which will allow us to pull in KeyVault secrets
manifest=$(
  cat <<EOF
apiVersion: secrets-store.csi.x-k8s.io/v1
kind: SecretProviderClass
metadata:
  name: "$kubernetes_tls_secret_name"
  namespace: traefik
spec:
  provider: azure
  secretObjects:
  - secretName: "$kubernetes_tls_secret_name"
    type: kubernetes.io/tls
    data:
      - objectName: "$tls_certificate_name"
        key: tls.key
      - objectName: "$tls_certificate_name"
        key: tls.crt
  parameters:
    useVMManagedIdentity: "true"
    userAssignedIdentityID: "$managed_identity_principal_id"
    keyvaultName: "$keyvault_name"
    objects: |
      array:
        - |
          objectName: "$tls_certificate_name"
          objectType: secret
    tenantId: "$tenant_id"
EOF
)

echo "$manifest" | kubectl apply -f -

# Setup dynamic configuration for Traefik, used below in deployment
manifest=$(
  cat <<EOF
apiVersion: v1
kind: ConfigMap
metadata:
  name: dynamic
  namespace: traefik
data:
  dynamic.toml: |
    # Dynamic configuration
    [[tls.certificates]]
    certFile = "/certs/tls.crt"
    keyFile = "/certs/tls.key"
EOF
)

echo "$manifest" | kubectl apply -f -

helm repo add traefik https://helm.traefik.io/traefik
helm upgrade --install traefik traefik/traefik --namespace traefik --create-namespace \
  -f - <<EOF  > /dev/null
logs:
  general:
    format: "json"
  access:
    enabled: "true"
    format: "json"
service:
  annotations: # We need to add the azure dns label, otherwise the public IP will not have a DNS name, which we need for cname record later.
    "service.beta.kubernetes.io/azure-dns-label-name": "eminence-${primary_cluster_name}-dns"
additionalArguments:
  - "--providers.file.filename=/config/dynamic.toml"
deployment:
  additionalVolumes:
    - name: secrets-store-inline
      secret:
        secretName: "$kubernetes_tls_secret_name"
    - name: secrets-store-direct
      csi:
        driver: secrets-store.csi.k8s.io
        readOnly: true
        volumeAttributes:
          secretProviderClass: "$kubernetes_tls_secret_name"
    - name: dynamic
      configMap:
        name: dynamic
additionalVolumeMounts:
  - name: secrets-store-inline
    mountPath: "/certs"
    readOnly: true
  - name: secrets-store-direct
    mountPath: "/certs-direct"
    readOnly: true
  - name: dynamic
    mountPath: "/config"
    readOnly: true
EOF

# Wait for public IP address
for wait in {0..10}; do
  public_ip=$(kubectl get -n traefik svc traefik -o json | jq -r .status.loadBalancer.ingress[0].ip)
  if [[ -z "$public_ip" || "$public_ip" == "null" ]]; then
    echo "Waiting ($wait) for public IP address..."
    sleep 5
  else
    echo "Public IP address: $public_ip"
    break
  fi
done

if [[ -z "$public_ip" ]]; then
  echo "Failed to get public IP address"
  exit 1
fi

####
# Set up storage accounts for each organization
####

for organization_name in $(echo "${environment_definition}" | jq -r '.organizations | keys[]'); do
  organization=$(echo "${environment_definition}" | jq --arg name "$organization_name" '.organizations[$name]')
  organization_resource_group=$(echo "${organization}" | jq -r '.resourceGroup')
  organization_namespace=$(echo "${organization}" | jq -r '.namespace')

  kubectl create namespace "${organization_namespace}" --dry-run=client -o yaml | kubectl apply -f -

  # Deploy storage accounts
  if [[ -n "${verbose:-}" ]]; then
    echo "Setting up storage accounts for organization ${organization_name}"
  fi

  az group create -l "${environment_region}" -n "${organization_resource_group}" >/dev/null

  for account in $(echo "${organization}" | jq -c '.storage.buffers + [.storage.storageServer] + [.storage.logs] | .[]'); do
    account_name=$(echo "${account}" | jq -r '.name')
    account_region=$(echo "${account}" | jq -r '.region')
    az storage account create -n "${account_name}" -g "${organization_resource_group}" -l "${account_region}" --only-show-errors -o none

    # Create or update storage secret
    # Note the dry-run -> apply is a "trick" that allow us to update the secret if it exists; "kubectl create secret ..." fails if secret exists
    connection_string="$(az storage account show-connection-string --name "${account_name}" | jq -r .connectionString)"
    kubectl create secret -n "${organization_namespace}" generic "${account_name}" --from-literal=connectionString="${connection_string}" --dry-run=client -o yaml | kubectl apply -f -
    if [[ "${account_name}" == "$(echo "${organization}" | jq -r '.storage.logs.name')" ]]; then
      az storage container create --name runs --connection-string "${connection_string}" >/dev/null
    fi
  done
done

# When deployment is complete, add the environment hash as a tag on the resource group
az tag update --operation Merge --resource-id "${environment_resource_group_id}" --tags "tygerdefinitionhash=${environment_hash}" -o none
