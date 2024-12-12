#!/usr/bin/env bash

# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

set -euo pipefail

container_name=tyger-test-ssh

ssh_port=2847
ssh_user=root # Run as root so we can easily test docker commands from within the container
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
    if [[ -f "${HOME}/.ssh/config" ]]; then
        sed -i "/$start_marker/,/$end_marker/d" "${HOME}/.ssh/config"
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

# Copy  bind mounts from tyger-local-gateway
gateway_bind_mounts=$(docker inspect -f '{{range .Mounts}}{{if eq .Type "bind"}}-v {{.Source}}:{{.Destination}} {{end}}{{end}}' tyger-local-gateway)

docker rm -f $container_name &>/dev/null

# shellcheck disable=SC2086
docker create \
    -p $ssh_port:22 \
    -e "SSH_ENABLE_ROOT=true" \
    -e "TCP_FORWARDING=true" \
    -v "/var/run/docker.sock:/var/run/docker.sock" \
    $gateway_bind_mounts \
    --name $container_name \
    quay.io/panubo/sshd:1.8.0 >/dev/null

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

# add docker cli to the container to test `tyger run create --pull``
mkdir -p /tmp/docker
docker_version=$(docker --version | grep -oP '\d+\.\d+\.\d+')
wget "https://download.docker.com/linux/static/stable/$(uname -m)/docker-$docker_version.tgz" -O /tmp/docker/docker.tgz -q
tar -C /tmp/docker -xzf /tmp/docker/docker.tgz
docker cp "/tmp/docker/docker/docker" "$container_name:/usr/bin/" >/dev/null
rm -rf /tmp/docker

docker start $container_name >/dev/null

docker exec $container_name chown $ssh_user:$ssh_user "/etc/authorized_keys/$ssh_user"

if [[ -n "${TYGER_ACCESSING_FROM_DOCKER:-}" ]]; then
    ssh_connection_host=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' $container_name)
    ssh_connection_port=22
else
    ssh_connection_host="localhost"
    ssh_connection_port=$ssh_port
fi

host_config="$start_marker
Host $ssh_host
  HostName $ssh_connection_host
  Port $ssh_connection_port
  User $ssh_user
  ControlMaster auto
  ControlPath  ~/.ssh/control-%C
  ControlPersist  yes
  ${config_identity_line:-}
$end_marker"

mkdir -p "${HOME}/.ssh"

cleanup_ssh_config
echo "$host_config" >>"${HOME}/.ssh/config"

touch "${HOME}/.ssh/known_hosts"
ssh-keygen -f "${HOME}/.ssh/known_hosts" -R "$ssh_connection_host"

max_attempts=30
attempts=0
until ssh $ssh_host -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null true &>/dev/null || [ $attempts -eq $max_attempts ]; do
    echo "Waiting for SSH server to be ready..."
    sleep 1
    attempts="$((attempts + 1))"
done

if [ $attempts -eq $max_attempts ]; then
    echo "Failed to connect to SSH server"
    exit 1
fi

echo "SSH server is ready"

if [[ -z ${start_only:-} ]]; then
    TYGER_CACHE_FILE=$(mktemp)
    export TYGER_CACHE_FILE
fi

tyger_socket_path="$(realpath "$(dirname "$0")/../install/local")/api.sock"

tyger login "ssh://${ssh_host}${tyger_socket_path}?option[StrictHostKeyChecking]=no"
tyger login status

if [[ -z ${start_only:-} ]]; then
    make -f "$(dirname "$0")/../Makefile" integration-test-no-up
fi
