.ONESHELL:
SHELL = /bin/bash
.SHELLFLAGS = -ecuo pipefail

.DEFAULT_GOAL := full

ENVIRONMENT_CONFIG_JSON = $(shell scripts/get-config.sh -o json | jq -c)
DEVELOPER_CONFIG_JSON = $(shell scripts/get-config.sh --dev -o json | jq -c)

SERVER_PATH=server/Tyger.Server
SECURITY_ENABLED=true
HELM_NAMESPACE=tyger
HELM_RELEASE=tyger
TYGER_URI = https://$(shell echo '${ENVIRONMENT_CONFIG_JSON}' | jq -r '.api.domainName')
INSTALL_CLOUD=false
AUTO_MIGRATE=true

get-environment-config:
	echo '${ENVIRONMENT_CONFIG_JSON}' | yq -P

ensure-environment: install-cli
	tyger cloud install -f <(scripts/get-config.sh)

ensure-environment-conditionally: install-cli
	if [[ "${INSTALL_CLOUD}" == "true" ]]; then
		tyger cloud install -f <(scripts/get-config.sh)
	fi

remove-environment: install-cli
	tyger cloud uninstall -f <(scripts/get-config.sh)

# Sets up the az subscription and kubectl config for the current environment
set-context:
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

	registry=$$(scripts/get-config.sh --dev -e .wipContainerRegistry.fqdn)
	buffer_sidecar_image="$$(docker inspect $${registry}/buffer-sidecar:dev | jq -r --arg repo $${registry}/buffer-sidecar '.[0].RepoDigests[] | select (startswith($$repo))')"
	worker_waiter_image="$$(docker inspect $${registry}/worker-waiter:dev | jq -r --arg repo $${registry}/worker-waiter '.[0].RepoDigests[] | select (startswith($$repo))')"

	jq <<- EOF > ${SERVER_PATH}/appsettings.local.json
		{
			"logging": { "Console": {"FormatterName": "simple" } },
			"auth": {
				"enabled": "${SECURITY_ENABLED}",
				"authority": "https://login.microsoftonline.com/$$(echo '${ENVIRONMENT_CONFIG_JSON}' | jq -r '.api.auth.tenantId')",
				"audience": "$$(echo '${ENVIRONMENT_CONFIG_JSON}' | jq -r '.api.auth.apiAppUri')",
				"cliAppUri": "$$(echo '${ENVIRONMENT_CONFIG_JSON}' | jq -r '.api.auth.cliAppUri')"
			},
			"kubernetes": {
				"kubeconfigPath": "$${HOME}/.kube/config",
				"namespace": "${HELM_NAMESPACE}",
				"jobServiceAccount": "${HELM_RELEASE}-job",
				"noOpConfigMap": "${HELM_RELEASE}-no-op",
				"workerWaiterImage": "$${worker_waiter_image}",
				"clusters": $$(echo '${ENVIRONMENT_CONFIG_JSON}' | jq -c '.cloud.compute.clusters')
			},
			"logArchive": {
				"storageAccountEndpoint": $$(echo $${helm_values} | jq -c '.server.logArchive.storageAccountEndpoint')
			},
			"buffers": {
				"storageAccounts": $$(echo $${helm_values} | jq -c '.server.buffers.storageAccounts'),
				"bufferSidecarImage": "$${buffer_sidecar_image}"
			},
			"database": {
				"connectionString": "Host=$$(echo $${helm_values} | jq -r '.server.database.host'); Database=$$(echo $${helm_values} | jq -r '.server.database.databaseName'); Port=$$(echo $${helm_values} | jq -r '.server.database.port'); Username=$$(az account show | jq -r '.user.name'); SslMode=VerifyFull",
				"autoMigrate": ${AUTO_MIGRATE} 
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

watch: set-localsettings
	cd ${SERVER_PATH}
	dotnet watch

unit-test:
	find . -name *csproj | xargs -L 1 dotnet test --no-restore -v q
	
	cd cli
	go test ./... | { grep -v "\\[[no test files\\]" || true; }

docker-build:
	if [[ "$${DO_NOT_BUILD_IMAGES:-}" == "true" ]]; then
		exit
	fi

	tag=$${EXPLICIT_IMAGE_TAG:-dev}

	registry=$$(scripts/get-config.sh --dev -e .wipContainerRegistry.fqdn)
	scripts/build-images.sh --push --push-force --tag "$$tag" --quiet --registry "$${registry}"

docker-build-test:
	if [[ "$${DO_NOT_BUILD_IMAGES:-}" == "true" ]]; then
		exit
	fi

	tag=$${EXPLICIT_IMAGE_TAG:-test}
	
	registry=$$(scripts/get-config.sh --dev -e .wipContainerRegistry.fqdn)
	scripts/build-images.sh --test --push --push-force --tag "$$tag" --quiet --registry "$${registry}"

publish-official-images:
	registry=$$(scripts/get-config.sh --dev -e .officialContainerRegistry.fqdn)
	tag=$$(git describe --tags)
	scripts/build-images.sh --push --push-force --helm --tag "$${tag}" --quiet --registry "$${registry}"

up: ensure-environment-conditionally docker-build
	repo_fqdn=$$(scripts/get-config.sh --dev -e .wipContainerRegistry.fqdn)

	if [[ -n "$${EXPLICIT_IMAGE_TAG:-}" ]]; then
		tyger_server_image="$${repo_fqdn}/tyger-server:$${EXPLICIT_IMAGE_TAG}"
		buffer_sidecar_image="$${repo_fqdn}/buffer-sidecar:$${EXPLICIT_IMAGE_TAG}"
		worker_waiter_image="$${repo_fqdn}/worker-waiter:$${EXPLICIT_IMAGE_TAG}"
	else
		tyger_server_image="$$(docker inspect "$${repo_fqdn}/tyger-server:dev" | jq -r --arg repo "$${repo_fqdn}/tyger-server" '.[0].RepoDigests[] | select (startswith($$repo))')"
		buffer_sidecar_image="$$(docker inspect "$${repo_fqdn}/buffer-sidecar:dev" | jq -r --arg repo "$${repo_fqdn}/buffer-sidecar" '.[0].RepoDigests[] | select (startswith($$repo))')"
		worker_waiter_image="$$(docker inspect "$${repo_fqdn}/worker-waiter:dev" | jq -r --arg repo "$${repo_fqdn}/worker-waiter" '.[0].RepoDigests[] | select (startswith($$repo))')"
	fi

	chart_dir=$$(readlink -f deploy/helm/tyger)
	
	tyger api install -f <(scripts/get-config.sh) \
		--set api.helm.tyger.chartRef="$${chart_dir}" \
		--set api.helm.tyger.values.server.image="$${tyger_server_image}" \
		--set api.helm.tyger.values.server.bufferSidecarImage="$${buffer_sidecar_image}" \
		--set api.helm.tyger.values.server.workerWaiterImage="$${worker_waiter_image}" \
		--set api.helm.tyger.values.server.database.autoMigrate=${AUTO_MIGRATE}

	$(MAKE) cli-ready

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

check-test-client-cert:
	cert_version=$$(echo '${DEVELOPER_CONFIG_JSON}'' | jq -r '.pemCertSecret.version')
	cert_path=$${HOME}/tyger_test_client_cert_$${cert_version}.pem
	[ -f ${TEST_CLIENT_CERT_FILE} ]

get-tyger-uri:
	echo ${TYGER_URI}

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

kill-proxy:
	killall tyger-proxy

login: install-cli download-test-client-cert
	tyger login "${TYGER_URI}"	

install-cli:
	official_container_registry=$$(scripts/get-config.sh --dev -e .officialContainerRegistry.fqdn)
	cd cli
	tag=$$(git describe --tags 2> /dev/null || echo "0.0.0")
	CGO_ENABLED=0 go install -ldflags="-s -w \
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
		--host="$$(echo $${helm_values} | jq -r '.server.database.host')" \
		--port="$$(echo $${helm_values} | jq -r '.server.database.port')" \
		--username="$$(az account show | jq -r '.user.name')" \
		--dbname="$$(echo $${helm_values} | jq -r '.server.database.databaseName')"

restore:
	cd cli
	go mod download
	cd ..
	find . -name *csproj | xargs -L 1 dotnet restore

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
