#! /bin/bash
#
# Gets the image property value from the devcontainer.json file.

set -euo pipefail
devcontainer_repository="compimagdevcontainers.azurecr.io/tyger"
regex_escaped_repository=${devcontainer_repository//\./\\.}

# The file is jsonc, so we resort to a regex to "parse" it.
grep -oP '(?<="image": ")'"${regex_escaped_repository}"'.*(?=")' "$(dirname "$0")/devcontainer.json"
