.ONESHELL:
SHELL = /bin/bash
.SHELLFLAGS = -ecuo pipefail

.DEFAULT_GOAL := full

# trick to lazily evaluate this at most once: https://make.mad-scientist.net/deferred-simple-variable-expansion/
ENVIRONMENT_CONFIG_JSON = $(eval ENVIRONMENT_CONFIG_JSON := $$(shell scripts/get-context-environment-config.sh -o json | jq -c))$(if $(ENVIRONMENT_CONFIG_JSON),$(ENVIRONMENT_CONFIG_JSON),$(error "get-context-environment-config.sh failed"))
DEVELOPER_CONFIG_JSON = $(eval DEVELOPER_CONFIG_JSON := $$(shell scripts/get-context-environment-config.sh -e developerConfig -o json | jq -c))$(if $(DEVELOPER_CONFIG_JSON),$(DEVELOPER_CONFIG_JSON),$(error "get-context-environment-config.sh failed"))

SERVER_PATH=server/Tyger.Server
SECURITY_ENABLED=true
HELM_NAMESPACE=tyger
HELM_RELEASE=tyger
TYGER_URI = https://$(shell echo '${ENVIRONMENT_CONFIG_JSON}' | jq -r '.api.domainName')
INSTALL_CLOUD=false

get-environment-config:
	echo '${ENVIRONMENT_CONFIG_JSON}' | yq -P

ensure-environment: install-cli
	tyger cloud install -f <(scripts/get-context-environment-config.sh)

ensure-environment-conditionally: install-cli
	if [[ "${INSTALL_CLOUD}" == "true" ]]; then
		tyger cloud install -f <(scripts/get-context-environment-config.sh)
	fi

remove-environment: install-cli
	tyger cloud uninstall -f <(scripts/get-context-environment-config.sh)

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
	if [[ $$(helm list -n "${HELM_NAMESPACE}" -l name=${HELM_RELEASE} -o json | jq length) == 0 ]]; then
		echo "Run 'make up' before this target"; exit 1
	fi

	buffer_secret_name=$$(echo '${ENVIRONMENT_CONFIG_JSON}' | jq -r '.cloud.storage.buffers[0].name')
	buffer_secret_value="$$(kubectl get secrets -n ${HELM_NAMESPACE} $${buffer_secret_name} -o jsonpath="{.data.connectionString}" | base64 -d)"

	logs_secret_name=$$(echo '${ENVIRONMENT_CONFIG_JSON}' | jq -r '.cloud.storage.logs.name')
	logs_secret_value="$$(kubectl get secrets -n ${HELM_NAMESPACE} $${logs_secret_name} -o jsonpath="{.data.connectionString}" | base64 -d)"

	postgres_password="$$(kubectl get secrets -n ${HELM_NAMESPACE} ${HELM_RELEASE}-db -o jsonpath="{.data.postgresql-password}" | base64 -d)"

	registry=$$(scripts/get-context-environment-config.sh -e developerConfig.wipContainerRegistry.fqdn)
	buffer_sidecar_image="$$(docker inspect $${registry}/buffer-sidecar:dev | jq -r --arg repo $${registry}/buffer-sidecar '.[0].RepoDigests[] | select (startswith($$repo))')"
	worker_waiter_image="$$(docker inspect $${registry}/worker-waiter:dev | jq -r --arg repo $${registry}/worker-waiter '.[0].RepoDigests[] | select (startswith($$repo))')"

	jq <<- EOF > ${SERVER_PATH}/appsettings.local.json
		{
			"logging": { "Console": {"FormatterName": "simple" } },
			"auth": {
				"enabled": "${SECURITY_ENABLED}",
				"authority":"https://login.microsoftonline.com/$$(echo '${ENVIRONMENT_CONFIG_JSON}' | jq -r '.api.auth.tenantId')",
				"audience":"$$(echo '${ENVIRONMENT_CONFIG_JSON}' | jq -r '.api.auth.apiAppUri')"
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
				"storageAccountConnectionString": "$${logs_secret_value}"
			},
			"buffers": {
				"connectionString": "$${buffer_secret_value}",
				"bufferSidecarImage": "$${buffer_sidecar_image}"
			},
			"database": {
				"connectionString": "Host=tyger-db; Database=tyger; Port=5432; Username=postgres; Password=$${postgres_password}"
			}
		}
	EOF

build: 
	find . -name *csproj | xargs -L 1 dotnet build
	
	cd cli
	go build ./...

build-server:
	cd ${SERVER_PATH}
	dotnet build --no-restore

run: check-forwarding set-localsettings
	cd ${SERVER_PATH}
	dotnet run -v m --no-restore

watch: check-forwarding set-localsettings
	cd ${SERVER_PATH}
	dotnet watch

unit-test:
	find . -name *csproj | xargs -L 1 dotnet test --no-restore -v q
	
	cd cli
	go test ./... | { grep -v "\\[[no test files\\]" || true; }

docker-build:
	registry=$$(scripts/get-context-environment-config.sh -e developerConfig.wipContainerRegistry.fqdn)
	scripts/build-images.sh --push --push-force --tag dev --quiet --registry "$${registry}"

docker-build-test:
	registry=$$(scripts/get-context-environment-config.sh -e developerConfig.wipContainerRegistry.fqdn)
	scripts/build-images.sh --test --push --push-force --tag test --quiet --registry "$${registry}"

publish-official-images:
	registry=$$(scripts/get-context-environment-config.sh -e developerConfig.officialContainerRegistry.fqdn)
	tag=$$(git describe --tags)
	scripts/build-images.sh --push --push-force --helm --tag "$${tag}" --quiet --registry "$${registry}"

up: ensure-environment-conditionally docker-build
	repo_fqdn=$$(scripts/get-context-environment-config.sh -e developerConfig.wipContainerRegistry.fqdn)

	tyger_server_image="$$(docker inspect "$${repo_fqdn}/tyger-server:dev" | jq -r --arg repo "$${repo_fqdn}/tyger-server" '.[0].RepoDigests[] | select (startswith($$repo))')"
	buffer_sidecar_image="$$(docker inspect "$${repo_fqdn}/buffer-sidecar:dev" | jq -r --arg repo "$${repo_fqdn}/buffer-sidecar" '.[0].RepoDigests[] | select (startswith($$repo))')"
	worker_waiter_image="$$(docker inspect "$${repo_fqdn}/worker-waiter:dev" | jq -r --arg repo "$${repo_fqdn}/worker-waiter" '.[0].RepoDigests[] | select (startswith($$repo))')"

	chart_dir=$$(readlink -f deploy/helm/tyger)
	
	tyger api install -f <(scripts/get-context-environment-config.sh) \
		--set api.helm.tyger.chartRef="$${chart_dir}" \
		--set api.helm.tyger.values.server.image="$${tyger_server_image}" \
		--set api.helm.tyger.values.server.bufferSidecarImage="$${buffer_sidecar_image}" \
		--set api.helm.tyger.values.server.workerWaiterImage="$${worker_waiter_image}"

	$(MAKE) cli-ready

down: install-cli
	tyger api uninstall -f <(scripts/get-context-environment-config.sh)

integration-test-no-up-prereqs: docker-build-test

integration-test-no-up: integration-test-no-up-prereqs cli-ready
	pushd cli/integrationtest
	go test -tags=integrationtest

integration-test: up integration-test-no-up-prereqs
	$(MAKE) integration-test-no-up-prereqs integration-test-no-up

e2e-no-up-prereqs: e2e-data
	
e2e-no-up: e2e-no-up-prereqs cli-ready
	pytest e2e --numprocesses 100 -q

e2e: up e2e-no-up-prereqs
	$(MAKE) e2e-no-up-prereqs e2e-no-up

dvc-data:
	scripts/check-login.sh
	dvc pull

gadgetron-data:
	python3 e2e/gadgetron/get_cases.py

e2e-data: dvc-data gadgetron-data

test: up unit-test integration-test e2e

full:
	$(MAKE) test INSTALL_CLOUD=true

test-no-up: unit-test integration-test-no-up e2e-no-up

forward:
	echo '${ENVIRONMENT_CONFIG_JSON}' | scripts/forward-services.sh -c -

check-forwarding:
	if ! curl "http://${HELM_RELEASE}-server:8080/healthcheck" &> /dev/null ; then
		echo "run 'make forward' in another terminal before running this target"
		exit 1
	fi


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
	allowedClientCIDRs: $$(hostname -i | awk '{print $$1"/32"}' | jq -c -R --slurp 'split("\n")[:-1]')
	logPath: "/tmp/tyger-proxy"
	EOF
	)

kill-proxy:
	killall tyger-proxy

login: install-cli download-test-client-cert
	tyger login "${TYGER_URI}"	

install-cli:
	official_container_registry=$$(scripts/get-context-environment-config.sh -e developerConfig.officialContainerRegistry.fqdn)
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
	postgres_password=$(shell kubectl get secrets ${HELM_RELEASE}-db -o jsonpath="{.data.postgresql-password}" | base64 -d)
	cmd="PGPASSWORD=$${postgres_password} psql -d tyger -U postgres"
	kubectl exec ${HELM_RELEASE}-db-0 -it -- bash -c "$${cmd}"

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
