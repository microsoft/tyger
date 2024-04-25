#!/usr/bin/env bash

# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -euo pipefail

container_name=tyger-test-ssh

ssh_port=2847
ssh_user=testuser
ssh_host=tygersshhost
start_marker="# START OF TYGER TESTING SECTION"
end_marker="# END OF TYGER TESTING SECTION"

while [[ $# -gt 0 ]]; do
    key="$1"

    case $key in
    --start-only)
        start_only=1
        shift
        ;;
    *)
        echo "ERROR: unknown option \"$key\""
        usage
        exit 1
        ;;
    esac
done

cleanup_ssh_config() {
    if [[ -f ~/.ssh/config ]]; then
        sed -i "/$start_marker/,/$end_marker/d" ~/.ssh/config
    fi
}

cleanup() {
    set +e
    cleanup_ssh_config
    docker rm -f $container_name >/dev/null
}

if [[ -z ${start_only:-} ]]; then
    trap cleanup SIGINT SIGTERM EXIT
fi

docker rm -f $container_name &>/dev/null
docker create \
    -p $ssh_port:22 \
    -e "SSH_USERS=$ssh_user:$(id -u):4000" \
    -e "SSH_GROUPS=tygerusers:4000" \
    -v /opt/tyger:/opt/tyger \
    --name $container_name \
    quay.io/panubo/sshd:1.6.0 >/dev/null

if [[ -z $(ssh-add -L >/dev/null || true) ]]; then
    priv_key_file=$(mktemp -u)
    pub_key_file="$priv_key_file.pub"

    ssh-keygen -t ed25519 -f "$priv_key_file" -N "" >/dev/null
    chmod 600 "$priv_key_file"
    docker cp "$pub_key_file" "$container_name:/etc/authorized_keys/$ssh_user" >/dev/null

    config_identity_line="IdentityFile $priv_key_file"
else
    pub_key_file=$(mktemp)
    ssh-add -L >"$pub_key_file"
    docker cp "$pub_key_file" "$container_name:/etc/authorized_keys/$ssh_user" >/dev/null
fi

docker cp "$(which tyger)" "$container_name:/usr/bin/" >/dev/null

docker start $container_name >/dev/null

host_config="$start_marker
Host $ssh_host
  HostName localhost
  Port $ssh_port
  User $ssh_user
  NoHostAuthenticationForLocalhost yes
  ControlMaster auto
  ControlPath  ~/.ssh/control-%C
  ControlPersist  yes
  ${config_identity_line:-}
$end_marker"

mkdir -p ~/.ssh
cleanup_ssh_config
echo "$host_config" >>~/.ssh/config

max_attempts=30
attempts=0
until ssh $ssh_host true &>/dev/null || [ $attempts -eq $max_attempts ]; do
    echo "Waiting for SSH server to be ready..."
    sleep 1
    attempts="$((attempts + 1))"
done

if [ $attempts -eq $max_attempts ]; then
    echo "Failed to connect to SSH server"
    exit 1
fi

echo "SSH server is ready"

TYGER_CACHE_FILE=$(mktemp)
export TYGER_CACHE_FILE

tyger login ssh://$ssh_host
tyger login status

if [[ -z ${start_only:-} ]]; then
    make -f "$(dirname "$0")/../Makefile" integration-test-no-up
fi
