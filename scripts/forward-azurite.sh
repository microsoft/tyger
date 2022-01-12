#! /bin/bash
#
# Run this script to forward localhost:10000 to the azurite pod running in the cluster that kubectl is connected to.
# This is useful for using tools like the Azure Storage VS Code extension

set -euo pipefail

kubectl get pods -o json \
  | jq -r '.items[] | select(.spec.containers[] | select(.image | startswith("mcr.microsoft.com/azure-storage/azurite"))) | .metadata.name' \
  | xargs -I{} kubectl port-forward {} 10000:10000
