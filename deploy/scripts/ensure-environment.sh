#! /bin/bash

set -euo pipefail

usage() {
  cat <<EOF

Deploys infrastructure required for an environment.

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

subscription=$(echo "${environment_definition}" | jq -r '.subscription')
previous_subscription=$(az account show --query name -o tsv)

if [[ "${subscription}" != "${previous_subscription}" ]]; then
  trap 'az account set --subscription "${previous_subscription}"' EXIT
  az account set --subscription "${subscription}"
fi

environment_region=$(echo "${environment_definition}" | jq -r '.defaultRegion')
environment_resource_group=$(echo "${environment_definition}" | jq -r '.resourceGroup')
acr=$(echo "${environment_definition}" | jq -r '.dependencies.containerRegistry')

az group create -l "${environment_region}" -n "${environment_resource_group}" >/dev/null

log_analytics_name=$(echo "${environment_definition}" | jq -r '.logAnalytics.name')
log_analytics_sku=$(echo "${environment_definition}" | jq -r '.logAnalytics.sku')

# Create a Log Analytics workspace for the environment
az monitor log-analytics workspace create -n "$log_analytics_name" -g "$environment_resource_group" --sku "${log_analytics_sku}" >/dev/null
workspace_id="$(az monitor log-analytics workspace show -n "$log_analytics_name" -g "$environment_resource_group" | jq -r .id)"

if [[ "$(az provider list --query "[?namespace=='Microsoft.ContainerService']" | jq '.[] | length > 0')" != "true" ]]; then
  echo "Attempting to register provider namespace. This will fail if you are not owner of subscription"
  az provider register --namespace Microsoft.ContainerService
fi

# Pinning aks-preview version due to bug
{ az extension add --name aks-preview --version 0.5.63 >/dev/null; } 2>&1

# We should not update this extension right now, since there is a bug forcing --min-count >= 1
# { az extension update --name aks-preview >/dev/null; } 2>&1

for cluster_name in $(echo "${environment_definition}" | jq -r '.clusters | keys[]'); do
  cluster=$(echo "${environment_definition}" | jq --arg name "$cluster_name" '.clusters[$name]')
  cluster_region=$(echo "$cluster" | jq -r '.region')
  system_node_size=$(echo "$cluster" | jq -r '.systemNodeSize')
  dns_prefix="${cluster_name}-dns"
  kubernetes_version="1.23.3"

  # Is this a create or update?
  if [[ -n "$(az aks list | jq --arg cluster "$cluster_name" '.[] | select(.name == $cluster)')" ]]; then
    az aks nodepool update -n system --cluster-name "$cluster_name" -g "$environment_resource_group" \
      --update-cluster-autoscaler \
      --min-count 1 \
      --max-count 3

    # Enable addons
    if [[ $(az aks show -n "$cluster_name" -g "$environment_resource_group" | jq -r '.addonProfiles.omsagent.enabled') != "true" ]]; then
      az aks enable-addons -a monitoring -n "$cluster_name" -g "$environment_resource_group" --workspace-resource-id "$workspace_id"
    fi
    if [[ $(az aks show -n "$cluster_name" -g "$environment_resource_group" | jq -r '.addonProfiles.azureKeyvaultSecretsProvider.enabled') != "true" ]]; then
      az aks enable-addons -a azure-keyvault-secrets-provider
    fi
  else
    az aks create \
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
      --generate-ssh-keys
  fi

  kubelet_objectId=$(az aks show -n "$cluster_name" -g "$environment_resource_group" | jq -r '.identityProfile.kubeletidentity.objectId')
  acr_id=$(az acr show -n "$acr" | jq -r '.id')
  if [[ $(az role assignment list --scope "${acr_id}" --role "AcrPull" --assignee "${kubelet_objectId}" | jq length) == 0 ]]; then
    # Attach ACR
    if [[ -n "${verbose:-}" ]]; then
      echo "Attaching container registry: $acr"
    fi

    az aks update -n "$cluster_name" -g "$environment_resource_group" --attach-acr "$acr"
  fi

  # Nodepools
  for pool_name in $(echo "${cluster}" | jq -r '.userNodePools | keys[]'); do
    pool=$(echo "${cluster}" | jq --arg name "$pool_name" '.userNodePools[$name]')
    min_count="$(echo "$pool" | jq -r .minCount)"
    max_count="$(echo "$pool" | jq -r .maxCount)"

    # Does the pool exist, if so update, otherwise create
    if [[ -n "$(az aks nodepool list --cluster-name "$cluster_name" -g "$environment_resource_group" | jq --arg pool "$pool_name" '.[] | select(.name == $pool)')" ]]; then
      if [[ -n "${verbose:-}" ]]; then
        echo "Updating node pool $pool_name"
      fi

      az aks nodepool update -n "$pool_name" --cluster-name "$cluster_name" -g "$environment_resource_group" \
        --update-cluster-autoscaler \
        --min-count "${min_count}" \
        --max-count "${max_count}"
    else
      if [[ -n "${verbose:-}" ]]; then
        echo "Creating node pool $pool_name"
      fi

      vm_size="$(echo "$pool" | jq -r .vmSize)"

      #If this is a GPU node pool, we will add some additional flags
      if [[ "${vm_size}" =~ "Standard_N" ]]; then
        az aks nodepool add -n "$pool_name" --cluster-name "$cluster_name" -g "$environment_resource_group" \
          --kubernetes-version "$kubernetes_version" \
          --enable-cluster-autoscaler \
          --node-count 1 \
          --min-count "${min_count}" \
          --max-count "${max_count}" \
          --node-vm-size "${vm_size}" \
          --labels tyger=run \
          --node-taints tyger=run:NoSchedule,sku=gpu:NoSchedule \
          --aks-custom-headers UseGPUDedicatedVHD=true
      else
        az aks nodepool add -n "$pool_name" --cluster-name "$cluster_name" -g "$environment_resource_group" \
          --kubernetes-version "$kubernetes_version" \
          --enable-cluster-autoscaler \
          --node-count 1 \
          --min-count "${min_count}" \
          --max-count "${max_count}" \
          --node-vm-size "${vm_size}" \
          --labels tyger=run \
          --node-taints tyger=run:NoSchedule
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
az keyvault set-policy -n "$keyvault_name" --secret-permissions get --object-id "$managed_identity_object_id" --subscription "${dependencies_subscription}"

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
  -f - <<EOF
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
    az storage account create -n "${account_name}" -g "${organization_resource_group}" -l "${account_region}"

    # Create or update storage secret
    # Note the dry-run -> apply is a "trick" that allow us to update the secret if it exists; "kubectl create secret ..." fails if secret exists
    connection_string="$(az storage account show-connection-string --name "${account_name}" | jq -r .connectionString)"
    kubectl create secret -n "${organization_namespace}" generic "${account_name}" --from-literal=connectionString="${connection_string}" --dry-run=client -o yaml | kubectl apply -f -
    if [[ "${account_name}" == "$(echo "${organization}" | jq -r '.storage.logs.name')" ]]; then
      az storage container create --name runs --connection-string "${connection_string}" > /dev/null
    fi
  done
done
