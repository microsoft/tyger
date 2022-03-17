#!/bin/bash

# This script cleans up orphaned role assignments in the BiomedicalImaging-NonProd cluster
# You must have user access administrator privileges to run this script

set -euo pipefail

az account set -s "BiomedicalImaging-NonProd"

# Service principals (e.g. in a pipeline) generally don't have access to read the directory details
# needed to run this script, so for now we will prevent anybody but a signed in user from running.
if [[ "$(az account show | jq -r .user.type)" != "user" ]]; then
  echo "This script should only be run by a user (not as a service principal)"
  exit 1
fi

# Clean assignments on the eminence ACR
acr_resource_id="$(az acr show -n eminence | jq -r .id)"
for sp in $(az role assignment list --scope "$acr_resource_id" | jq -r '.[] | select(.principalType=="ServicePrincipal") | .principalId'); do
  if [[ -z "$(az ad sp show --id "${sp}" 2>/dev/null)" ]]; then
    az role assignment delete --ids "$(az role assignment list --scope "$acr_resource_id" --query "[?principalId=='$sp']" | jq -r .[].id)"
  fi
done
