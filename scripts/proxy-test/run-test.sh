#! /bin/bash

set -euo pipefail

this_dir="$(readlink -f "$( dirname "$0")")"

docker compose create --build

docker compose start squid

buffer_id=$(tyger buffer create)

config=$("${this_dir}/../get-config.sh" -o json)
tyger_uri="https://$(echo "$config" | jq -r '.api.domainName')"
dev_config=$("${this_dir}/../get-config.sh" --dev -o json)

test_app_uri="$(echo "$dev_config" | jq -r '.testAppUri')"
cert_file_path="$HOME/tyger_test_client_cert_$(echo "$dev_config" | jq -r '.pemCertSecret.version').pem"

cred_file=$(mktemp)
{
    echo "serverUri: ${tyger_uri}"
    echo "servicePrincipal: ${test_app_uri}"
    echo "certificatePath: /client_cert.pem"
    echo "logPath: /logs"
} >> "$cred_file"


docker compose cp "$(which tyger)" tyger-proxy:/usr/local/bin
docker compose cp "$(which tyger)" mars:/usr/local/bin
docker compose cp "$(which tyger-proxy)" tyger-proxy:/usr/local/bin
docker compose cp "$cert_file_path" tyger-proxy:/client_cert.pem
docker compose cp "$cred_file" tyger-proxy:/creds.yml
docker compose start tyger-proxy

proxy="squid:3128"

# Prep the cred file

# Test tyger using the squid proxy
docker compose exec -T tyger-proxy bash -c "if curl -s --fail https://microsoft.com; then echo 'expected call to fail without a proxy configured'; exit 1; fi"
docker compose exec -T tyger-proxy bash -c "if ! curl -s --proxy ${proxy} --fail https://microsoft.com; then echo 'expected call to succeed with a proxy configured'; exit 1; fi"
docker compose exec -T tyger-proxy bash -c "export HTTPS_PROXY=${proxy}; tyger login -f /creds.yml && tyger buffer access ${buffer_id} > /dev/null"

# now start up the tyger proxy
# docker compose exec -T tyger-proxy bash -c "export HTTPS_PROXY=${proxy}; tyger-proxy start -f /creds.yml"

docker compose start mars

# docker compose kill
# docker compose down
# docker compose rm
