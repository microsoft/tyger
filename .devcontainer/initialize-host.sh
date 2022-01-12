#! /bin/bash

set -euo pipefail

create-kubeconfig-snapshot() {
    mkdir -p ~/.kube-snapshot
    kubectl config view --raw >~/.kube-snapshot/config
}

if command -v kubectl >/dev/null; then
    cluster_name=$(kubectl config view -o json | jq -r '.clusters | .[] | select(.cluster.server | (startswith("https://kubernetes.docker.internal") or startswith("https://127.0.0"))).name' 2> /dev/null || true )
    if [ -n "$cluster_name" ]; then
        echo "An existing local cluster was found"
        create-kubeconfig-snapshot
        exit 0
    fi
fi

echo "Installing k3s..."
"$(dirname "$0")"/install-k3s.sh

create-kubeconfig-snapshot

echo "k3s installed"
