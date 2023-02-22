#! /bin/bash

set -euo pipefail

# yq
YQ_VERSION=v4.16.2
YQ_BINARY=yq_linux_amd64

wget https://github.com/mikefarah/yq/releases/download/${YQ_VERSION}/${YQ_BINARY}.tar.gz -O - |\
  tar xz && mv ${YQ_BINARY} /usr/bin/yq


# oras
ORAS_VERSION="0.2.1-alpha.1"

wget "https://github.com/oras-project/oras/releases/download/v${ORAS_VERSION}/oras_${ORAS_VERSION}_linux_amd64.tar.gz" -O - |\
  tar xz && mv oras /usr/bin/

# tv
TV_VERSION="v0.7.0"
wget "https://github.com/uzimaru0000/tv/releases/download/${TV_VERSION}/tv-x86_64-unknown-linux-gnu.zip" \
&& unzip tv-x86_64-unknown-linux-gnu.zip \
&& mv tv-x86_64-unknown-linux-gnu/tv /usr/bin

# CUE
CUE_VERSION=v0.4.3
go install cuelang.org/go/cmd/cue@${CUE_VERSION}
