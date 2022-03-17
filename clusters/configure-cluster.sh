#!/bin/bash

set -euo pipefail

keyvault_name="eminence"
keyvault_cert_secret_name="tyger-tls"
keyvault_cert_secret_version="17931206ec624a70b657a67eda461b0c"
eminence_tls_secret_name="eminence-tls"

aks_clusters=$(kubectl config view -o json | jq -r '.clusters | .[] | select(.cluster.server | contains("azmk8s.io")).name')

if [ -z "$aks_clusters" ]; then
  echo "There are no AKS clusters in the current kubeconfig"
  exit 1
fi

current_context=$(kubectl config current-context)
current_cluster=$(kubectl config view -o json | jq -r --arg context "${current_context}" '.contexts | .[] | select(.name == $context).context.cluster')

# If we don't currently have an AKS cluster selected, pick the first one
if [[ ! "$aks_clusters" =~ $current_cluster ]]; then
  current_cluster=$(echo "$aks_clusters" | head -n1)
  context_name=$(kubectl config view -o json | jq -r --arg cluster_name "${current_cluster}" '.contexts | .[] | select(.context.cluster == $cluster_name).name')

  echo "You have not selected an AKS cluster. Select one with:"
  echo "  kubectl config use-context $context_name"
  exit 1
fi

####
# Set up CSI to get secrets from KeyVault
####

managed_identity_principal_id=$(az aks list -o json | jq -r --arg cluster "$current_cluster" '.[] | select(.name == $cluster).addonProfiles.azureKeyvaultSecretsProvider.identity.clientId')
managed_identity_object_id=$(az aks list -o json | jq -r --arg cluster "$current_cluster" '.[] | select(.name == $cluster).addonProfiles.azureKeyvaultSecretsProvider.identity.objectId')
tenant_id=$(az aks list -o json | jq -r --arg cluster "$current_cluster" '.[] | select(.name == $cluster).identity.tenantId')

# We have to use object-id here because the principal id (--spn argument) fails in devops because the SP there does not have graph access
az keyvault set-policy -n "$keyvault_name" --secret-permissions get --object-id  "$managed_identity_object_id"

###
# Set up traefik
###

# Create the namespace if it does not exits:
if [ -z "$(kubectl get namespaces -o json | jq --arg ns traefik '.items[] | select(.metadata.name == $ns)')" ]; then
  kubectl create namespace traefik
fi

# Set up the SecretProviderClass which will allow us to pull in KeyVault secrets
manifest=$(cat << EOF
apiVersion: secrets-store.csi.x-k8s.io/v1
kind: SecretProviderClass
metadata:
  name: "$eminence_tls_secret_name"
  namespace: traefik
spec:
  provider: azure
  secretObjects:
  - secretName: "$eminence_tls_secret_name"
    type: kubernetes.io/tls
    data:
      - objectName: "$keyvault_cert_secret_name"
        key: tls.key
      - objectName: "$keyvault_cert_secret_name"
        key: tls.crt
  parameters:
    useVMManagedIdentity: "true"
    userAssignedIdentityID: "$managed_identity_principal_id"
    keyvaultName: "$keyvault_name"
    objects: |
      array:
        - |
          objectName: "$keyvault_cert_secret_name"
          objectType: secret
          objectVersion: "$keyvault_cert_secret_version"
    tenantId: "$tenant_id"
EOF
)

echo "$manifest" | kubectl apply -f -


# Setup dynamic configuration for Traefik, used below in deployment
manifest=$(cat << EOF
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
    "service.beta.kubernetes.io/azure-dns-label-name": "eminence-${current_cluster}-dns"
additionalArguments:
  - "--providers.file.filename=/config/dynamic.toml"
deployment:
  additionalVolumes:
    - name: secrets-store-inline
      secret:
        secretName: "$eminence_tls_secret_name"
    - name: secrets-store-direct
      csi:
        driver: secrets-store.csi.k8s.io
        readOnly: true
        volumeAttributes:
          secretProviderClass: "$eminence_tls_secret_name"
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
