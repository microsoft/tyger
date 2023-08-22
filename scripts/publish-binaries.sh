#!/bin/bash

set -euo pipefail

SCRIPT=$(realpath "$0")
SOURCE_DIR=$(dirname "$SCRIPT")/..
DIST_DIR="${SOURCE_DIR}/dist"

# Create distribution location
mkdir -p "${DIST_DIR}"
rm -rf "${DIST_DIR:?}/*"
mkdir -p "${DIST_DIR}/linux/amd64"
mkdir -p "${DIST_DIR}/windows/amd64"

COMMIT_HASH=$(git rev-parse --short HEAD)

push=0
force=0
storage_account="tygerdist"
storage_container=""

# parse command line arguments
while [[ $# -gt 0 ]]; do
  key="$1"

  case $key in
  --tag)
    tag="$2"
    shift 2
    ;;
  --storage-account)
    storage_account="$2"
    shift 2
    ;;
  --storage-container)
    storage_container="$2"
    shift 2
    ;;
  --push)
    push=1
    shift
    ;;
  --push-force)
    push=1
    force=1
    shift
    ;;
  --use-git-hash-as-container-name)
    if [ -z "$(git status --porcelain -uno)" ]; then
      storage_container="$(git rev-parse HEAD)"
    else
      echo "Git working directory is not clean. Please commit your changes before using the --use-git-hash-as-container-name option."
      exit 1
    fi
    shift
    ;;
  -h | --help)
    usage
    exit 0
    ;;
  *)
    echo "Unknown option: $key"
    usage
    exit 1
    ;;
  esac
done

# Build go binaries
cd "${SOURCE_DIR}/cli"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w -X main.commit=${COMMIT_HASH}" -v -o "${DIST_DIR}/linux/amd64/tyger" ./cmd/tyger
CGO_ENABLED=1 GOOS=windows GOARCH=amd64 go build -ldflags="-s -w -X main.commit=${COMMIT_HASH}" -v -o "${DIST_DIR}/windows/amd64/tyger.exe" ./cmd/tyger
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w -X main.commit=${COMMIT_HASH}" -v -o "${DIST_DIR}/linux/amd64/tyger-proxy" ./cmd/tyger-proxy
CGO_ENABLED=1 GOOS=windows GOARCH=amd64 go build -ldflags="-s -w -X main.commit=${COMMIT_HASH}" -v -o "${DIST_DIR}/windows/amd64/tyger-proxy.exe" ./cmd/tyger-proxy

# .NET binaries
cd "${SOURCE_DIR}/tools/Transform-Xml/Transform-Xml"
dotnet publish -r win-x64 -p:PublishSingleFile=true -p:DebugType=None -p:DebugSymbols=false --self-contained true -c release -o "${DIST_DIR}/windows/amd64/"
dotnet publish -r linux-x64 -p:PublishSingleFile=true -p:DebugType=None -p:DebugSymbols=false --self-contained true -c release -o "${DIST_DIR}/linux/amd64/"

# If push is enabled, push the binaries to the storage account
if [ "$push" -eq 1 ]; then
    if [ -z "$storage_container" ]; then
        echo "Storage container name is required"
        exit 1
    fi

    # Check if the container exists
    if [ "$force" -eq 1 ]; then
        az storage container create --auth-mode login --account-name "$storage_account" --name "$storage_container" 1>/dev/null
    else
        az storage container create --auth-mode login --account-name "$storage_account" --name "$storage_container" --fail-on-exist 1>/dev/null
    fi

    az storage blob upload-batch --auth-mode login --overwrite --account-name "$storage_account" --destination "$storage_container" --source "${DIST_DIR}" --pattern "*"
fi
