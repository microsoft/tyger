#!/bin/bash
#
# This script initializes the devcontainer after it has been created.

set -euo pipefail

# allow kubectl to port forward from port <1024
sudo setcap CAP_NET_BIND_SERVICE=+eip /opt/conda/envs/tyger/bin/kubectl

# download go and nuget packages
make -f "$(dirname "$0")/../Makefile" restore

# trust the dotnet dev cert
dotnet dev-certs https
sudo -E "$(which dotnet)" dev-certs https -ep /usr/local/share/ca-certificates/aspnet/https.crt --format PEM
sudo update-ca-certificates

if [ -d "/home/.host/.azure/" ]; then
    # Copy the Azure CLI context cache directory that is bind-mounted from the host.
    # This means that an "az login" will not be necessary in the devcontainer if the user has already
    # logged in on the host.
    cp -r /home/.host/.azure/ ~/.azure/
fi
