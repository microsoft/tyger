# Copyright (c) Microsoft Corporation.
# Licensed under the MIT License.

.ONESHELL:
SHELL = /bin/bash
.SHELLFLAGS = -ecuo pipefail

.DEFAULT_GOAL := full

ENVIRONMENT_CONFIG_JSON = $(shell scripts/get-config.sh -o json | jq -c)
DEVELOPER_CONFIG_JSON = $(shell scripts/get-config.sh --dev -o json | jq -c)

SERVER_PATH=server/ControlPlane
SECURITY_ENABLED=true
HELM_NAMESPACE=tyger
HELM_RELEASE=tyger
TYGER_URI = https://$(shell echo '${ENVIRONMENT_CONFIG_JSON}' | jq -r '.api.domainName')
INSTALL_CLOUD=false
AUTO_MIGRATE=false

check-az-login:
	scripts/check-az-login.sh

az-login-from-host:
	scripts/az-login-from-host.sh

get-config:
	echo '${ENVIRONMENT_CONFIG_JSON}' | yq -P

ensure-environment: check-az-login install-cli
	tyger cloud install -f <(scripts/get-config.sh)

ensure-environment-conditionally: install-cli
	if [[ "${INSTALL_CLOUD}" == "true" ]]; then
		$(MAKE) ensure-environment
	fi

remove-environment: install-cli
	tyger cloud uninstall -f <(scripts/get-config.sh)

# Sets up the az subscription and kubectl config for the current environment
set-context: check-az-login
	subscription=$$(echo '${ENVIRONMENT_CONFIG_JSON}' | jq -r '.cloud.subscriptionId')
	resource_group=$$(echo '${ENVIRONMENT_CONFIG_JSON}' | jq -r '.cloud.resourceGroup')

	for cluster in $$(echo '${ENVIRONMENT_CONFIG_JSON}' | jq -c '.cloud.compute.clusters | .[]'); do
		if [[ "$$(echo "$$cluster" | jq -r '.apiHost')" == "true" ]]; then
			cluster_name=$$(echo "$$cluster" | jq -r '.name')
			az account set --subscription "$${subscription}"
			az aks get-credentials -n "$${cluster_name}" -g "$${resource_group}" --overwrite-existing --only-show-errors
			kubelogin convert-kubeconfig -l azurecli
			kubectl config set-context --current --namespace=${HELM_NAMESPACE}
		fi
	done

set-localsettings:
	helm_values=$$(helm get values -n ${HELM_NAMESPACE} ${HELM_RELEASE} -o json || true)

	if [[ -z "$${helm_values}" ]]; then
		echo "Run 'make up' and 'make set-context' before this target"; exit 1
	fi

	jq <<- EOF > ${SERVER_PATH}/appsettings.local.json
		{
			"logging": { "Console": {"FormatterName": "simple" } },
			"serviceMetadata": {
				"externalBaseUrl": "http://localhost:5000"
			},
			"auth": {
				"enabled": "${SECURITY_ENABLED}",
				"authority": "https://login.microsoftonline.com/$$(echo '${ENVIRONMENT_CONFIG_JSON}' | jq -r '.api.auth.tenantId')",
				"audience": "$$(echo '${ENVIRONMENT_CONFIG_JSON}' | jq -r '.api.auth.apiAppUri')",
				"cliAppUri": "$$(echo '${ENVIRONMENT_CONFIG_JSON}' | jq -r '.api.auth.cliAppUri')"
			},
			"compute": {
				"kubernetes": {
					"kubeconfigPath": "$${HOME}/.kube/config",
					"namespace": "${HELM_NAMESPACE}",
					"jobServiceAccount": "${HELM_RELEASE}-job",
					"noOpConfigMap": "${HELM_RELEASE}-no-op",
					"workerWaiterImage": "$$(echo '${ENVIRONMENT_CONFIG_JSON}' | jq -r '.api.helm.tyger.values.workerWaiterImage')",
					"clusters": $$(echo '${ENVIRONMENT_CONFIG_JSON}' | jq -c '.cloud.compute.clusters'),
					"currentPodUid": "00000000-0000-0000-0000-000000000000"
				}
			},
			"logArchive": {
				"cloudStorage": {
					"storageAccountEndpoint": $$(echo $${helm_values} | jq -c '.logArchive.storageAccountEndpoint')
				}
			},
			"buffers": {
				"cloudStorage": {
					"storageAccounts": $$(echo $${helm_values} | jq -c '.buffers.storageAccounts')
				},
				"bufferSidecarImage": "$$(echo '${ENVIRONMENT_CONFIG_JSON}' | jq -r '.api.helm.tyger.values.bufferSidecarImage')"
			},
			"database": {
				"connectionString": "Host=$$(echo $${helm_values} | jq -r '.database.host'); Database=$$(echo $${helm_values} | jq -r '.database.databaseName'); Port=$$(echo $${helm_values} | jq -r '.database.port'); Username=$$(az account show | jq -r '.user.name'); SslMode=VerifyFull",
				"autoMigrate": ${AUTO_MIGRATE},
				"tygerServerRoleName": "$$(echo $${helm_values} | jq -r '.identity.tygerServer.name')"
			}
		}
	EOF

local-docker-set-localsettings:
	run_secrets_path="/opt/tyger/secrets/runs"
	mkdir -p "$${run_secrets_path}"
	logs_path="/opt/tyger/logs"
	mkdir -p "$${logs_path}"

	jq <<- EOF > ${SERVER_PATH}/appsettings.local.json
		{
			"urls": "http://unix:/opt/tyger/tyger.sock",
			"logging": { "Console": {"FormatterName": "simple" } },
			"serviceMetadata": {
				"externalBaseUrl": "http+unix:///opt/tyger/tyger.sock:"
			},
			"auth": {
				"enabled": "false"
			},
			"compute": {
				"docker": {
					"runSecretsPath": "$${run_secrets_path}"
				}
			},
			"logArchive": {
				"localStorage": {
					"logsDirectory": "$${logs_path}"
				}
			},
			"buffers": {
				"localStorage": {
					"dataDirectory": "/opt/tyger/buffers",
					"primarySigningCertificatePath": "/opt/tyger/secrets/tyger_local_buffer_service_cert_$$(echo '${DEVELOPER_CONFIG_JSON}' | jq -r '.localBufferServiceCertSecret.version').pem"
				},
				"bufferSidecarImage": "$$(echo '${ENVIRONMENT_CONFIG_JSON}' | jq -r '.api.helm.tyger.values.bufferSidecarImage')"
			},
			"database": {
				"connectionString": "Host=localhost; Port=5432; Username=tyger-server",
				"passwordFile": "/opt/tyger/secrets/db_password.txt",
				"autoMigrate": "true",
				"tygerServerRoleName": "tyger-server"
			}
		}
	EOF

build-csharp:
	find . -name *csproj | xargs -L 1 dotnet build

build-go:
	cd cli
	go build -v ./...

build: build-csharp build-go

build-server:
	cd ${SERVER_PATH}
	dotnet build --no-restore

run: set-localsettings
	cd ${SERVER_PATH}
	dotnet run -v m --no-restore

local-docker-run: local-docker-set-localsettings download-local-buffer-service-cert
	cd ${SERVER_PATH}
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
	scripts/build-images.sh $$target_arg --push --push-force --tag "$$tag" --registry "$${registry}"

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
	scripts/build-images.sh --push --push-force --tyger-server --worker-waiter --buffer-sidecar --helm --tag "$${tag}" --registry "$${registry}"

up: ensure-environment-conditionally docker-build-tyger-server docker-build-buffer-sidecar docker-build-worker-waiter
	tyger api install -f <(scripts/get-config.sh)
	$(MAKE) cli-ready

local-docker-up:
	cd local-docker

	# if secrets/db_password.txt is not present or empty, generate a new password
	if [[ ! -s /opt/tyger/secrets/db_password.txt ]]; then
		openssl rand -base64 36 > /opt/tyger/secrets/db_password.txt
	fi

	docker compose up -d --wait

migrate: ensure-environment-conditionally docker-build-tyger-server
	tyger api migrations apply --latest --wait -f <(scripts/get-config.sh)

down: install-cli
	tyger api uninstall -f <(scripts/get-config.sh)

integration-test-no-up-prereqs: docker-build-test

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

full:
	$(MAKE) test INSTALL_CLOUD=true

test-no-up: unit-test integration-test-no-up

download-test-client-cert:
	cert_version=$$(echo '${DEVELOPER_CONFIG_JSON}' | jq -r '.pemCertSecret.version')
	cert_path=$${HOME}/tyger_test_client_cert_$${cert_version}.pem
	if [[ ! -f "$${cert_path}" ]]; then
		rm -f "$${cert_path}"
		subscription=$$(echo '${ENVIRONMENT_CONFIG_JSON}' | yq '.cloud.subscriptionId')
		vault_name=$$(echo '${DEVELOPER_CONFIG_JSON}' | jq -r '.keyVault')
		cert_name=$$(echo '${DEVELOPER_CONFIG_JSON}' | jq -r '.pemCertSecret.name')
		cert_version=$$(echo '${DEVELOPER_CONFIG_JSON}' | jq -r '.pemCertSecret.version')
		az keyvault secret download --vault-name "$${vault_name}" --name "$${cert_name}" --version "$${cert_version}" --file "$${cert_path}" --subscription "$${subscription}"
		chmod 600 "$${cert_path}"
	fi

download-local-buffer-service-cert:
	mkdir -p /opt/tyger/secrets
	cert_version=$$(echo '${DEVELOPER_CONFIG_JSON}' | jq -r '.localBufferServiceCertSecret.version')
	cert_path=/opt/tyger/secrets/tyger_local_buffer_service_cert_$${cert_version}.pem
	if [[ ! -f "$${cert_path}" ]]; then
		rm -f "$${cert_path}"
		subscription=$$(echo '${ENVIRONMENT_CONFIG_JSON}' | yq '.cloud.subscriptionId')
		vault_name=$$(echo '${DEVELOPER_CONFIG_JSON}' | jq -r '.keyVault')
		cert_name=$$(echo '${DEVELOPER_CONFIG_JSON}' | jq -r '.localBufferServiceCertSecret.name')
		cert_version=$$(echo '${DEVELOPER_CONFIG_JSON}' | jq -r '.localBufferServiceCertSecret.version')
		az keyvault secret download --vault-name "$${vault_name}" --name "$${cert_name}" --version "$${cert_version}" --file "$${cert_path}" --subscription "$${subscription}"
		chmod 600 "$${cert_path}"
	fi

check-test-client-cert:
	cert_version=$$(echo '${DEVELOPER_CONFIG_JSON}'' | jq -r '.pemCertSecret.version')
	cert_path=$${HOME}/tyger_test_client_cert_$${cert_version}.pem
	[ -f ${TEST_CLIENT_CERT_FILE} ]

get-tyger-uri:
	echo ${TYGER_URI}

login-local-docker:
	tyger login http+unix:///opt/tyger/tyger.sock:

login-service-principal: install-cli download-test-client-cert
	cert_version=$$(echo '${DEVELOPER_CONFIG_JSON}' | jq -r '.pemCertSecret.version')
	cert_path=$${HOME}/tyger_test_client_cert_$${cert_version}.pem
	test_app_uri=$$(echo '${DEVELOPER_CONFIG_JSON}' | jq -r '.testAppUri')

	tyger login -f <(cat <<EOF
	serverUri: ${TYGER_URI}
	servicePrincipal: $${test_app_uri}
	certificatePath: $${cert_path}
	EOF
	)

start-proxy: install-cli download-test-client-cert
	cert_version=$$(echo '${DEVELOPER_CONFIG_JSON}' | jq -r '.pemCertSecret.version')
	cert_path=$${HOME}/tyger_test_client_cert_$${cert_version}.pem
	test_app_uri=$$(echo '${DEVELOPER_CONFIG_JSON}' | jq -r '.testAppUri')

	tyger-proxy start -f <(cat <<EOF
	serverUri: ${TYGER_URI}
	servicePrincipal: $${test_app_uri}
	certificatePath: $${cert_path}
	allowedClientCIDRs: ["127.0.0.1/32"]
	logPath: "/tmp/tyger-proxy"
	EOF
	)

run-local-docker-proxy:
	tyger-proxy run -f <(cat <<EOF
	serverUri: "http+unix:///opt/tyger/tyger.sock:"
	logPath: "/tmp/tyger-proxy"
	EOF
	)

kill-proxy:
	killall tyger-proxy

login: install-cli download-test-client-cert
	tyger login "${TYGER_URI}"	

install-cli:
	official_container_registry=$$(scripts/get-config.sh --dev -e .officialContainerRegistry.fqdn)
	cd cli
	tag=$$(git describe --tags 2> /dev/null || echo "0.0.0")
	CGO_ENABLED=0 go install -buildvcs=false -ldflags="-s -w \
		-X main.version=$${tag} \
		-X github.com/microsoft/tyger/cli/internal/install.containerRegistry=$${official_container_registry} \
		-X github.com/microsoft/tyger/cli/internal/install.containerImageTag=$${tag}" \
		./cmd/tyger ./cmd/buffer-sidecar ./cmd/tyger-proxy

cli-ready: install-cli
	if ! tyger login status &> /dev/null; then
		$(MAKE) login-service-principal
	fi

connect-db: set-context
	helm_values=$$(helm get values -n ${HELM_NAMESPACE} ${HELM_RELEASE} -o json || true)

	if [[ -z "$${helm_values}" ]]; then
		echo "Run 'make up' before this target"; exit 1
	fi

	export PGPASSWORD=$$(az account get-access-token --resource-type oss-rdbms | jq -r .accessToken)
	
	psql \
		--host="$$(echo $${helm_values} | jq -r '.database.host')" \
		--port="$$(echo $${helm_values} | jq -r '.database.port')" \
		--username="$$(az account show | jq -r '.user.name')" \
		--dbname="$$(echo $${helm_values} | jq -r '.database.databaseName')"

restore:
	cd cli
	go mod download
	cd ../server
	dotnet restore

format:
	find . -name *csproj | xargs -L 1 dotnet format

verify-format:
	find . -name *csproj | xargs -i sh -c 'dotnet build -p:EnforceCodeStyleInBuild=true {} && dotnet format --verify-no-changes {}'

purge-runs: set-context
	for pod in $$(kubectl get pod -n "${HELM_NAMESPACE}" -l tyger-run -o go-template='{{range .items}}{{.metadata.name}}{{"\n"}}{{end}}'); do
		kubectl patch pod -n "${HELM_NAMESPACE}" "$${pod}" \
			--type json \
			--patch='[ { "op": "remove", "path": "/metadata/finalizers" } ]'
	done
	kubectl delete job,statefulset,secret,service -n "${HELM_NAMESPACE}" -l tyger-run --cascade=foreground

start-docs-website:
	cd docs
	npm install
	npm run docs:dev
