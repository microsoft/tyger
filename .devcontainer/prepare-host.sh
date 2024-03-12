#!/bin/bash

set -euo pipefail

# Check if /opt/tyger exists. If if does, exit. Otherwise, create it. This will require sudo.
# The ownership should be the same as if it sudo hasn't been used.

if [ ! -d /opt/tyger ]; then
    uid=$(id -u)
    gid=$(id -g)

    sudo mkdir /opt/tyger
    sudo chown -R "$uid":"$gid" /opt/tyger
fi
