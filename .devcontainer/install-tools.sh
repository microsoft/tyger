#!/usr/bin/env bash

# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -euo pipefail

# yq
YQ_VERSION=v4.53.6
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
KUBELOGIN_VERSION=0.2.19
KUBELOGIN_ARCHIVE="kubelogin-linux-$(dpkg --print-architecture).zip"
wget "https://github.com/Azure/kubelogin/releases/download/v${KUBELOGIN_VERSION}/${KUBELOGIN_ARCHIVE}" \
  "https://github.com/Azure/kubelogin/releases/download/v${KUBELOGIN_VERSION}/${KUBELOGIN_ARCHIVE}.sha256"
sha256sum --check "${KUBELOGIN_ARCHIVE}.sha256"
unzip -j "${KUBELOGIN_ARCHIVE}" -d /usr/local/bin

# install psql
install -d /usr/share/postgresql-common/pgdg
wget --quiet https://www.postgresql.org/media/keys/ACCC4CF8.asc -O /usr/share/postgresql-common/pgdg/apt.postgresql.org.asc
echo "deb [signed-by=/usr/share/postgresql-common/pgdg/apt.postgresql.org.asc] https://apt.postgresql.org/pub/repos/apt $(lsb_release -cs)-pgdg main" > /etc/apt/sources.list.d/pgdg.list
apt-get update
DEBIAN_FRONTEND=noninteractive apt-get -y install --no-install-recommends \
  postgresql-client-18

# install az-pim-cli
AZ_PIM_CLI_VERSION=1.1.0
AZ_PIM_DIR="az-pim-cli-${AZ_PIM_CLI_VERSION}-linux-$(dpkg --print-architecture)"
wget https://github.com/netr0m/az-pim-cli/releases/download/v${AZ_PIM_CLI_VERSION}/${AZ_PIM_DIR}.tar.gz -O - |\
  tar xz && sudo mv "${AZ_PIM_DIR}/az-pim-cli" /usr/bin
