#!/bin/bash

set -euo pipefail
curl -sfL https://get.k3s.io | sh -s - --docker --no-deploy traefik --write-kubeconfig-mode 644
