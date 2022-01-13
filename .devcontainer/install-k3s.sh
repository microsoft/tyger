#!/bin/bash

set -euo pipefail
curl -sfL https://get.k3s.io | sh -s - --docker --no-deploy traefik --write-kubeconfig-mode 600
sudo chown "$USER:$USER" /etc/rancher/k3s/k3s.yaml
