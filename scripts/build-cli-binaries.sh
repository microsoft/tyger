#!/bin/bash

set -euo pipefail

thisdir="$(dirname "$0")"
currentdir="$(pwd)"

if [ -z "${BUILD_ARTIFACTSTAGINGDIRECTORY:-}" ]; then
    OUTPUTDIR="${currentdir}/cli-export"
else
    OUTPUTDIR="${BUILD_ARTIFACTSTAGINGDIRECTORY}/cli-export"
fi
mkdir -p "${OUTPUTDIR}/linux"
mkdir -p "${OUTPUTDIR}/windows"

cd "${thisdir}/../cli"
GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o "${OUTPUTDIR}/linux/tyger" -v ./cmd/tyger
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o "${OUTPUTDIR}/windows/tyger.exe" -v ./cmd/tyger
