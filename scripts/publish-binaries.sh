#!/bin/bash

set -euo pipefail

source_dir="$(dirname "$0")/.."
dist_dir="${source_dir}/dist"
# Create distribution location
mkdir -p "${dist_dir}"
rm -rf "${dist_dir:?}/*"
mkdir -p "${dist_dir}/linux/amd64"

COMMIT_HASH=$(git rev-parse --short HEAD)

echo $source_dir
exit 0

# Build go binaries
cd "${source_dir}/cli"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w -X main.commit=${COMMIT_HASH}" -v -o "${dist_dir}/linux/amd64/tyger" ./cmd/tyger
CGO_ENABLED=1 GOOS=windows GOARCH=amd64 go build -ldflags="-s -w -X main.commit=${COMMIT_HASH}" -v -o "${dist_dir}/windows/amd64/tyger.exe" ./cmd/tyger
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w -X main.commit=${COMMIT_HASH}" -v -o "${dist_dir}/linux/amd64/tyger-proxy" ./cmd/tyger-proxy
CGO_ENABLED=1 GOOS=windows GOARCH=amd64 go build -ldflags="-s -w -X main.commit=${COMMIT_HASH}" -v -o "${dist_dir}/windows/amd64/tyger-proxy.exe" ./cmd/tyger-proxy
