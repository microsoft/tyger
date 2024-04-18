#!/bin/bash

set -euo pipefail

uid=$(id -u)
gid=$(id -g)

# Check if /opt/tyger exists. If if does, exit. Otherwise, create it. This will require sudo.
# The ownership should be the same as if it sudo hasn't been used.

if [ ! -d /opt/tyger ]; then
    sudo mkdir /opt/tyger
    sudo chown -R "$uid":"$gid" /opt/tyger
fi

if [ ! -d /tmp/tyger ]; then
    mkdir -m 777 /tmp/tyger
fi
