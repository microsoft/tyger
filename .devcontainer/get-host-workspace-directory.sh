#! /bin/bash
#
# Determines the bind-mounted repo directory on the host

set -euo pipefail

container_info=$(docker container inspect "$(docker ps -q --filter ancestor="$("$(dirname "${0}")/get-devcontainer-image.sh")")")
container_workspace_directory="$(readlink -f "$(dirname "${0}")/..")/"
for mount in $(echo "$container_info" | jq -c '.[0].Mounts | .[] | select(.Type == "bind")'); do
    source=$(echo "$mount" | jq -r '.Source + "/"')
    target=$(echo "$mount" | jq -r '.Destination + "/"')

    if [[ "${container_workspace_directory}" == "${target}"* ]]; then
        source="${source}${container_workspace_directory:${#target}}"
        echo "${source%/}"
        exit
    fi
done

echo "ERROR: unable to determine host workspace directory"
exit 1
