#! /bin/bash

# This tests that requests can flow from from a tyger CLI client to a tyger proxy, through a squid proxy, and to a server.

set -euo pipefail

this_dir="$(readlink -f "$( dirname "$0")")"

docker compose create --build

docker compose start squid

buffer_id=$(tyger buffer create)
echo "hi" | tyger buffer write "${buffer_id}"

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

# Load binaries and credentials into the containers
docker compose cp "$(which tyger)" tyger-proxy:/usr/local/bin
docker compose cp "$(which tyger)" client:/usr/local/bin
docker compose cp "$(which tyger-proxy)" tyger-proxy:/usr/local/bin
docker compose cp "$cert_file_path" tyger-proxy:/client_cert.pem
docker compose cp "$cred_file" tyger-proxy:/creds.yml
docker compose start tyger-proxy

proxy="squid:3128"

# Test tyger using the squid proxy
docker compose exec -T tyger-proxy bash -c "if curl -s --fail https://microsoft.com; then echo 'expected call to fail without a proxy configured'; exit 1; fi"
docker compose exec -T tyger-proxy bash -c "if ! curl -s --proxy ${proxy} --fail https://microsoft.com; then echo 'expected call to succeed with a proxy configured'; exit 1; fi"
docker compose exec -T tyger-proxy bash -c "export HTTPS_PROXY=${proxy}; tyger login -f /creds.yml && tyger buffer read ${buffer_id} > /dev/null"
# specify proxy in creds.yml
docker compose exec -T tyger-proxy bash -c "echo 'proxy: ${proxy}' >> /creds.yml"
docker compose exec -T tyger-proxy bash -c "tyger login -f /creds.yml && tyger buffer read ${buffer_id} > /dev/null"


# Now start up the tyger proxy
docker compose exec -T tyger-proxy bash -c "tyger-proxy start -f /creds.yml"

# And connect to it from a client and verify that requests flow from client -> tyger-proxy -> squid -> server
docker compose start client
docker compose exec -T client bash -c "tyger login http://tyger-proxy:6888"
docker compose exec -T client bash -c "tyger buffer read ${buffer_id} > /dev/null"

# Now repeat without TLS certificate validation

echo -e "\nRemoving TLS root certificates and disabling TLS certificate validation\n"
docker compose exec -T tyger-proxy bash -c "echo '' > /etc/ssl/certs/ca-certificates.crt"
docker compose exec -T tyger-proxy bash -c "echo 'disableTlsCertificateValidation: true' >> /creds.yml"
docker compose exec -T tyger-proxy bash -c "export HTTPS_PROXY=${proxy}; tyger login -f /creds.yml && tyger buffer read ${buffer_id} > /dev/null"
docker compose exec -T tyger-proxy bash -c "echo 'proxy: ${proxy}' >> /creds.yml"
docker compose exec -T tyger-proxy bash -c "tyger login -f /creds.yml && tyger buffer read ${buffer_id} > /dev/null"
docker compose exec -T tyger-proxy bash -c "pgrep tyger-proxy | xargs kill"
docker compose exec -T tyger-proxy bash -c "export HTTPS_PROXY=${proxy}; tyger-proxy start -f /creds.yml"

docker compose exec -T client bash -c "echo '' > /etc/ssl/certs/ca-certificates.crt"
docker compose exec -T client bash -c "tyger login http://tyger-proxy:6888 --disable-tls-certificate-validation"
docker compose exec -T client bash -c "tyger buffer read ${buffer_id} > /dev/null"

docker compose kill
docker compose down
docker compose rm
