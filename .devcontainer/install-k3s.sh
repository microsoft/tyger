#!/bin/bash

set -euo pipefail
curl -sfL https://get.k3s.io | sh -s - --docker --write-kubeconfig-mode 600
sudo chown "$USER:$(user -g)" /etc/rancher/k3s/k3s.yaml

# Here we add any additional config changes to traefik
chart_config=$(cat << EOF
apiVersion: helm.cattle.io/v1
kind: HelmChartConfig
metadata:
  name: traefik
  namespace: kube-system
spec:
  valuesContent: |-
    logs:
      general:
        format: "json"
      access:
        enabled: "true"
        format: "json"
EOF
)

echo "$chart_config" | sudo tee /var/lib/rancher/k3s/server/manifests/traefik-config.yaml


# Make sure changes take effect
sudo systemctl restart k3s
