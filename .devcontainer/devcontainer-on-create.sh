#!/bin/bash
#
# This script initializes the devcontainer after it has been created.

set -euo pipefail

# download go and nuget packages
make -f "$(dirname "$0")/../Makefile" restore || true

if [ -d "/home/.host/.azure/" ]; then
    # Copy the Azure CLI context cache directory that is bind-mounted from the host.
    # This means that an "az login" will not be necessary in the devcontainer if the user has already
    # logged in on the host.
    cp -r /home/.host/.azure/ ~/.azure/
fi

make install-cli || true
