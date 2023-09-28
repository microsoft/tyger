#! /bin/bash

set -euo pipefail

usage() {
  cat <<EOF

A script to help with development. Invokes kubectl port-forward on all services and all pods with hostname and subdomain set in a namespace.
It also temporarily adds entries into /etc/hosts to match the DNS names that Kubernetes gives these objects. The result is that you can call
these endpoints from your development environment as if you were running in a pod in the cluster.

Usage: $0 [options]

Options:
  -c | --environment-config     The environment configuration JSON file or - to read from stdin
  -h, --help                    Brings up this menu
EOF
}

namespace="tyger"

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

tmp_kubeconfig=$(mktemp)
export KUBECONFIG="${tmp_kubeconfig}"
original_hosts=$(cat /etc/hosts)

function revert() {
  rm -f "${tmp_kubeconfig}"
  sudo echo "${original_hosts}" | sudo tee /etc/hosts >/dev/null
  kill 0
}

trap revert EXIT

environment_definition=$(cat "${config_path}")

subscription=$(echo "${environment_definition}" | jq -r '.cloud.subscriptionId')
resource_group=$(echo "${environment_definition}" | jq -r '.cloud.resourceGroup // .environmentName')

for cluster in $(echo "${environment_definition}" | jq -c '.cloud.compute.clusters | .[]'); do
  if [[ "$(echo "${cluster}" | jq -r '.apiHost')" == "true" ]]; then
    cluster_name=$(echo "${cluster}" | jq -r '.name')
    az aks get-credentials --overwrite-existing --subscription "${subscription}" --resource-group "${resource_group}" --name "${cluster_name}" --only-show-errors
    kubelogin convert-kubeconfig --login azurecli
  fi
done

mapfile -t services_to_forward < <(kubectl get svc -n "${namespace}" -l "tyger!=run" -o json | jq -r -c '.items[] | select(.spec.type == "ClusterIP" and .spec.clusterIP != "None" and .spec.selector) |  { "name": .metadata.name, "ports": [.spec.ports | .[] | .port] } ')

declare -a forwards

i=1
for svc in "${services_to_forward[@]}"; do
  ((i = i + 1))
  name=$(echo "${svc}" | jq -r '.name')
  ip="127.0.0.${i}"
  echo "${ip} ${name}.${namespace}.svc.cluster.local ${name}.${namespace} ${name}" | sudo tee -a /etc/hosts >/dev/null
  mapfile -t ports < <(echo "${svc}" | jq -r '.ports | .[]')
  for port in "${ports[@]}"; do
    kubectl port-forward -n "${namespace}" "svc/${name}" --address "${ip}" "${port}:${port}" | tail -n +2 &
  done

  forwards+=("Forwarding ${name} [${ports[*]}] ${ip}")
done

mapfile -t pods_to_forward < <(kubectl get pods -n "${namespace}" -l "tyger!=run" -o json | jq -r -c '.items[] | select(.spec.hostname and .spec.subdomain) | { "name": .metadata.name, "hostname": .spec.hostname, "subdomain": .spec.subdomain, "ports": [.spec.containers | .[] | .ports | .[] | .containerPort] } ')
for pod in "${pods_to_forward[@]}"; do
  ((i = i + 1))
  name=$(echo "${pod}" | jq -r '.name')
  subdomain=$(echo "${pod}" | jq -r '.subdomain')
  hostname=$(echo "${pod}" | jq -r '.hostname')
  ip="127.0.0.${i}"
  echo "${ip} ${hostname}.${subdomain}.${namespace}.svc.cluster.local ${hostname}.${subdomain}.${namespace} ${hostname}.${subdomain}" | sudo tee -a /etc/hosts >/dev/null
  mapfile -t ports < <(echo "${pod}" | jq -r '.ports | .[]')
  for port in "${ports[@]}"; do
    kubectl port-forward -n "${namespace}" "pod/${name}" --address "${ip}" "${port}:${port}" | tail -n +2 &
  done

  forwards+=("Forwarding ${name} [${ports[*]}]")
done

if [[ -z "${forwards[*]}" ]]; then
  echo "Nothing found."
  exit 0
else
  printf '%s\n' "${forwards[@]}" | sort
  sleep infinity
fi
