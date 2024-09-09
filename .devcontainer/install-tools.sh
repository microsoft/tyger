#!/usr/bin/env bash

# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -euo pipefail

# yq
YQ_VERSION=v4.35.1
YQ_BINARY="yq_linux_$(dpkg --print-architecture)"

wget "https://github.com/mikefarah/yq/releases/download/${YQ_VERSION}/${YQ_BINARY}.tar.gz" -O - |\
  tar xz && mv "${YQ_BINARY}" /usr/bin/yq

# tv
TV_VERSION="v0.7.0"
TV_ARCHIVE="tv-$(uname -m)-unknown-linux-gnu"
wget "https://github.com/uzimaru0000/tv/releases/download/${TV_VERSION}/${TV_ARCHIVE}.zip" \
&& unzip "${TV_ARCHIVE}".zip \
&& mv "${TV_ARCHIVE}/tv" /usr/bin

# install kubelogin
KUBELOGIN_VERSION=0.0.33
sudo az aks install-cli --kubelogin-version "${KUBELOGIN_VERSION}" --install-location "/dev/null"

# install psql
echo "deb https://apt.postgresql.org/pub/repos/apt $(lsb_release -cs)-pgdg main" > /etc/apt/sources.list.d/pgdg.list
wget --quiet -O - https://www.postgresql.org/media/keys/ACCC4CF8.asc | sudo apt-key add -
apt-get update
DEBIAN_FRONTEND=noninteractive apt-get -y install --no-install-recommends \
  postgresql-client-16

# install az-pim
AZ_PIM_VERSION=0.5.0
wget "https://github.com/demoray/azure-pim-cli/releases/download/0.5.0/az-pim-linux-musl-${AZ_PIM_VERSION}" \
&& sudo mv "az-pim-linux-musl-${AZ_PIM_VERSION}" /usr/bin/az-pim \
&& sudo chmod +x /usr/bin/az-pim
