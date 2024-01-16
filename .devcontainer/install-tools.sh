#! /bin/bash

set -euo pipefail

# yq
YQ_VERSION=v4.35.1
YQ_BINARY=yq_linux_amd64

wget https://github.com/mikefarah/yq/releases/download/${YQ_VERSION}/${YQ_BINARY}.tar.gz -O - |\
  tar xz && mv ${YQ_BINARY} /usr/bin/yq

# tv
TV_VERSION="v0.7.0"
wget "https://github.com/uzimaru0000/tv/releases/download/${TV_VERSION}/tv-x86_64-unknown-linux-gnu.zip" \
&& unzip tv-x86_64-unknown-linux-gnu.zip \
&& mv tv-x86_64-unknown-linux-gnu/tv /usr/bin

# install kubelogin
KUBELOGIN_VERSION=0.0.33
sudo az aks install-cli --kubelogin-version "${KUBELOGIN_VERSION}" --install-location "/dev/null"

# install psql
echo "deb https://apt.postgresql.org/pub/repos/apt $(lsb_release -cs)-pgdg main" > /etc/apt/sources.list.d/pgdg.list
wget --quiet -O - https://www.postgresql.org/media/keys/ACCC4CF8.asc | sudo apt-key add -
apt-get update
DEBIAN_FRONTEND=noninteractive apt-get -y install --no-install-recommends \
  postgresql-client-16
