#!/bin/bash

set -euo pipefail

for c in "$(dirname "$0")"/configs/*.json; do
  echo "Validating cluster config: $c"
  "$(dirname "$0")"/get-validated-cluster-definition.sh "$c" >/dev/null
done
