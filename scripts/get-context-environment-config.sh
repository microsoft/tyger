#! /bin/bash

set -euo pipefail

this_dir=$(dirname "${0}")

"${this_dir}/../deploy/scripts/get-environment-config.sh" -d "${this_dir}/../deploy/config/dev/" -e "$("${this_dir}"/get-context-environment-name.sh)"
