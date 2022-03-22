#! /bin/bash

set -euo pipefail

usage()
{
  cat << EOF

A script to help with development. Invokes kubectl port-forward on all services and all pods with hostname and subdomain set in a namespace.
It also temporarily adds entries into /etc/hosts to match the DNS names that Kubernetes gives these objects. The result is that you can call
these endpoints from your development environment as if you were running in a pod in the cluster.

Usage: $0 [options]

Options:
  --namespace,-n <namespace>    The namespace of services and pods to forward to. Defaults to $HELM_NAMESPACE defined in envrc.
  -h, --help                    Brings up this menu
EOF
}

namespace=$(kubectl config view -o json | jq -r '.contexts | .[] | select(.name == "default").context.namespace')

while [[ $# -gt 0 ]]; do
  key="$1"

  case $key in
    --namespace|-n)
      namespace="${2}"
      shift
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


original_hosts=$(cat /etc/hosts)

function revert()
{
  sudo echo "${original_hosts}" | sudo tee /etc/hosts > /dev/null
  kill 0
}

trap revert EXIT

mapfile -t services_to_forward < <(kubectl get svc -n "${namespace}" -o json | jq -r -c '.items[] | select(.spec.type == "ClusterIP" and .spec.clusterIP != "None" and .spec.selector) |  { "name": .metadata.name, "ports": [.spec.ports | .[] | .port] } ')

declare -a forwards

i=1
for svc in "${services_to_forward[@]}"; do
  ((i=i+1))
  name=$(echo "${svc}" | jq -r '.name')
  ip="127.0.0.${i}"
  echo "${ip} ${name}.${namespace}.svc.cluster.local ${name}.${namespace} ${name}" | sudo tee -a /etc/hosts > /dev/null
  mapfile -t ports < <(echo "${svc}" | jq -r '.ports | .[]')
  for port in "${ports[@]}"; do
    kubectl port-forward -n "${namespace}" "svc/${name}" --address "${ip}" "${port}:${port}" | tail -n +2 &
  done

  forwards+=("Forwarding ${name} [${ports[*]}] ${ip}")
done

mapfile -t pods_to_forward < <(kubectl get pods -n "${namespace}" -o json | jq -r -c '.items[] | select(.spec.hostname and .spec.subdomain) | { "name": .metadata.name, "hostname": .spec.hostname, "subdomain": .spec.subdomain, "ports": [.spec.containers | .[] | .ports | .[] | .containerPort] } ')
for pod in "${pods_to_forward[@]}"; do
  ((i=i+1))
  name=$(echo "${pod}" | jq -r '.name')
  subdomain=$(echo "${pod}" | jq -r '.subdomain')
  hostname=$(echo "${pod}" | jq -r '.hostname')
  ip="127.0.0.${i}"
  echo "${ip} ${hostname}.${subdomain}.${namespace}.svc.cluster.local ${hostname}.${subdomain}.${namespace} ${hostname}.${subdomain}" | sudo tee -a /etc/hosts > /dev/null
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
