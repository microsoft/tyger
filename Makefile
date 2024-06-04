# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

.ONESHELL:
SHELL = /bin/bash
.SHELLFLAGS = -ecuo pipefail

.DEFAULT_GOAL := full

CONTROL_PLANE_SERVER_PATH=server/ControlPlane
DATA_PLANE_SERVER_PATH=server/DataPlane

DEVELOPER_CONFIG_JSON = $(shell scripts/get-config.sh --dev -o json | jq -c)

ifeq ($(TYGER_ENVIRONMENT_TYPE),)
include Makefile.cloud
else ifeq ($(TYGER_ENVIRONMENT_TYPE),cloud)
include Makefile.cloud
else ifeq ($(TYGER_ENVIRONMENT_TYPE),docker)
include Makefile.docker
endif

get-config:
	echo '${ENVIRONMENT_CONFIG_JSON}' | yq -P

open-docker-window:
	code /workspaces/tyger-docker

open-cloud-window:
	code /workspaces/tyger

check-az-login:
	scripts/check-az-login.sh

az-login-from-host:
	scripts/az-login-from-host.sh

build-csharp:
	find . -name *csproj | xargs -L 1 dotnet build

build-go:
	cd cli
	go build -v ./...

build: build-csharp build-go

run: set-localsettings
	cd server/ControlPlane
	dotnet run -v m --no-restore

unit-test:
	find . -name *csproj | xargs -L 1 dotnet test --no-restore -v q
	
	cd cli
	go test ./... | { grep -v "\\[[no test files\\]" || true; }

_docker-build:
	if [[ "$${DO_NOT_BUILD_IMAGES:-}" == "true" ]]; then
		exit
	fi

	if [[ -z "${DOCKER_BUILD_TARGET}" ]]; then
		echo "DOCKER_BUILD_TARGET not set"
		exit 1
	fi

	target_arg="--${DOCKER_BUILD_TARGET}"

	tag=$${EXPLICIT_IMAGE_TAG:-dev}

	registry=$$(scripts/get-config.sh --dev -e .wipContainerRegistry.fqdn)
	scripts/build-images.sh $$target_arg ${DOCKER_BUILD_ARCH_FLAGS} ${DOCKER_BUILD_PUSH_FLAGS} --tag "$$tag" --registry "$${registry}"

docker-build-test:
	$(MAKE) _docker-build DOCKER_BUILD_TARGET=test-connectivity

docker-build-tyger-server:
	$(MAKE) _docker-build DOCKER_BUILD_TARGET=tyger-server

docker-build-buffer-sidecar:
	$(MAKE) _docker-build DOCKER_BUILD_TARGET=buffer-sidecar

docker-build-worker-waiter:
	$(MAKE) _docker-build DOCKER_BUILD_TARGET=worker-waiter

docker-build: docker-build-test docker-build-tyger-server docker-build-buffer-sidecar docker-build-worker-waiter

publish-official-images:
	registry=$$(scripts/get-config.sh --dev -e .officialContainerRegistry.fqdn)
	tag=$$(git describe --tags)
	scripts/build-images.sh --push --push-force --arch amd64 --arch arm64 --tyger-server --worker-waiter --buffer-sidecar --helm --tag "$${tag}" --registry "$${registry}"

integration-test-no-up-prereqs:

integration-test-no-up: integration-test-no-up-prereqs cli-ready
	if [[ -n "$${EXPLICIT_IMAGE_TAG:-}" ]]; then
		repo_fqdn=$$(scripts/get-config.sh --dev -e .wipContainerRegistry.fqdn)
		export TEST_CONNECTIVITY_IMAGE="$${repo_fqdn}/testconnectivity:$${EXPLICIT_IMAGE_TAG}"
	fi

	pushd cli/integrationtest
	go test -tags=integrationtest

integration-test: up integration-test-no-up-prereqs
	$(MAKE) integration-test-no-up-prereqs integration-test-no-up

test: up unit-test integration-test
	$(MAKE) variant-test

test-no-up: unit-test integration-test-no-up
	$(MAKE) variant-test

full:
	$(MAKE) test INSTALL_CLOUD=true

get-tyger-uri:
	echo ${TYGER_URI}

install-cli:
	official_container_registry=$$(scripts/get-config.sh --dev -e .officialContainerRegistry.fqdn)
	cd cli
	tag=$$(git describe --tags 2> /dev/null || echo "0.0.0")
	CGO_ENABLED=0 go install -buildvcs=false -ldflags="-s -w \
		-X main.version=$${tag} \
		-X github.com/microsoft/tyger/cli/internal/install.ContainerRegistry=$${official_container_registry} \
		-X github.com/microsoft/tyger/cli/internal/install.ContainerImageTag=$${tag}" \
		./cmd/tyger ./cmd/buffer-sidecar ./cmd/tyger-proxy

cli-ready: install-cli
	if ! tyger login status &> /dev/null; then
		$(MAKE) login
	fi

restore:
	cd cli
	go mod download
	cd ../server
	dotnet restore

format:
	cd server
	dotnet format

verify-format:
	cd server
	dotnet build -p:EnforceCodeStyleInBuild=true
	dotnet format --verify-no-changes

start-docs-website:
	cd docs
	npm install
	npm run docs:dev
