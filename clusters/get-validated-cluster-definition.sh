#!/bin/bash

set -euo pipefail

cluster_config="$1"

if [[ ! "$cluster_config" =~ .json$ ]]; then
  # This is the name of a cluster, so we will try to locate it
  for config_file in "$(dirname "$0")"/configs/*.json; do
    if [[ "$(jq -r .clusterName < "$config_file")" == "$cluster_config" ]]; then
      cluster_config="$config_file"
      break
    fi
  done
fi

if [[ ! "$cluster_config" =~ .json$ ]]; then
  echo "$cluster_config is not a cluster config file or the name of a cluster"
  exit 1
fi

schema_file="$(dirname "$0")/cluster-schema.json"
default_config="$(dirname "$0")/default-cluster-definition.json"

# Merge the specific cluster config onto the default
merged_config=$(jq -s '.[0] * .[1]' "${default_config}" "${cluster_config}")

# Validate cluster configuration
echo "${merged_config}" | jsonschema "${schema_file}"

echo "$merged_config"
