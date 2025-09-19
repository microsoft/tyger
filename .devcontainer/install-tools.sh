#!/usr/bin/env bash

# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -euo pipefail

# yq
YQ_VERSION=v4.47.2
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
KUBELOGIN_VERSION=0.2.10
sudo az aks install-cli --kubelogin-version "${KUBELOGIN_VERSION}" --install-location "/dev/null"

# install psql
echo "deb https://apt.postgresql.org/pub/repos/apt $(lsb_release -cs)-pgdg main" > /etc/apt/sources.list.d/pgdg.list
wget --quiet -O - https://www.postgresql.org/media/keys/ACCC4CF8.asc | sudo apt-key add -
apt-get update
DEBIAN_FRONTEND=noninteractive apt-get -y install --no-install-recommends \
  postgresql-client-16

# install az-pim-cli
AZ_PIM_CLI_VERSION=1.1.0
AZ_PIM_DIR="az-pim-cli-${AZ_PIM_CLI_VERSION}-linux-$(dpkg --print-architecture)"
wget https://github.com/netr0m/az-pim-cli/releases/download/v${AZ_PIM_CLI_VERSION}/${AZ_PIM_DIR}.tar.gz -O - |\
  tar xz && sudo mv "${AZ_PIM_DIR}/az-pim-cli" /usr/bin
