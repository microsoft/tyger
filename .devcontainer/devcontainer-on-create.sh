#!/bin/bash
#
# This script initializes the devcontainer after it has been created.
# Copies over the kubectl context from the host and sets the context and prepares the development environment.

set -euo pipefail

mkdir -p "${HOME}/.kube"
if [ -d "${HOME}/.kube-host" ]; then
    cp -r "${HOME}/.kube-host"/* "${HOME}/.kube"
fi

if [[ -f "${HOME}/.kube/config" ]]; then
    chmod 600 "${HOME}/.kube/config"
fi

cluster_name=$(kubectl config view -o json | jq -r '.clusters | .[] | select(.cluster.server | (startswith("https://kubernetes.docker.internal") or startswith("https://127.0.0"))).name')
context_name=$(kubectl config view -o json | jq -r --arg cluster_name "${cluster_name}" '.contexts | .[] | select(.context.cluster == $cluster_name).name')

if [ -z "${context_name}" ]; then
    echo "WARNING: unable to find kubectl context pointing to a local cluster"
else
    kubectl config use-context "${context_name}"
fi

helm_namespace=$(make -s get-namespace)

kubectl create namespace "${helm_namespace}" --dry-run=client -o yaml | kubectl apply -f -
kubectl config set-context --current --namespace="${helm_namespace}"

# allow kubectl to port forward from port <1024
sudo setcap CAP_NET_BIND_SERVICE=+eip /opt/conda/envs/tyger/bin/kubectl

# download go and nuget packages
make -f "$(dirname "$0")/../Makefile" restore

# trust the dotnet dev cert
dotnet dev-certs https
sudo -E "$(which dotnet)" dev-certs https -ep /usr/local/share/ca-certificates/aspnet/https.crt --format PEM
sudo update-ca-certificates
