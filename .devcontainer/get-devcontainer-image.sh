#! /bin/bash
#
# Gets the image property value from the devcontainer.json file.

set -euo pipefail
# shellcheck source=envrc
source "$(dirname "$0")/../envrc"

regex_escaped_repository=${DEVCONTAINER_REPOSITORY//\./\\.}

# The file is jsonc, so we resort to a regex to "parse" it.
grep -oP '(?<="image": ")'"${regex_escaped_repository}"'.*(?=")' "${REPO_ROOT}"/.devcontainer/devcontainer.json
