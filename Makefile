.ONESHELL:
SHELL = /bin/bash
.SHELLFLAGS = -ecuo pipefail

.DEFAULT_GOAL := full

# trick to lazily evaluate this at most once: https://make.mad-scientist.net/deferred-simple-variable-expansion/
ENVIRONMENT_CONFIG_JSON = $(eval ENVIRONMENT_CONFIG := $$(shell scripts/get-context-environment-config.sh -o json))$(if $(ENVIRONMENT_CONFIG),$(ENVIRONMENT_CONFIG),$(error "get-context-environment-config.sh failed"))

SERVER_PATH=server/Tyger.Server
SECURITY_ENABLED=true
HELM_NAMESPACE=tyger
HELM_RELEASE=tyger
TEST_CLIENT_CERT_VERSION=1db664a6a3c74b6f817f3d842424003d
TEST_CLIENT_CERT_FILE=$${HOME}/tyger_test_client_cert_${TEST_CLIENT_CERT_VERSION}.pem
TEST_CLIENT_IDENTIFIER_URI=api://tyger-test-client
TYGER_URI = https://$(shell echo '${ENVIRONMENT_CONFIG_JSON}' | jq -r '.api.domainName')
SHOULD_INSTALL_CLOUD=false


get-environment-config:
	echo '${ENVIRONMENT_CONFIG_JSON}' | yq -P

install-cloud: install-cli-tyger-only
	tyger install cloud -f <(scripts/get-context-environment-config.sh)

install-cloud-conditionally: install-cli-tyger-only
	if [[ "${SHOULD_INSTALL_CLOUD}" == "true" ]]; then
		tyger install cloud -f <(scripts/get-context-environment-config.sh)
	fi

uninstall-cloud: install-cli-tyger-only
	tyger uninstall cloud -f <(scripts/get-context-environment-config.sh)

# Sets up the az subscription and kubectl config for the current environment
set-context:
	subscription=$$(echo '${ENVIRONMENT_CONFIG_JSON}' | jq -r '.cloud.subscriptionId')
	resource_group=$$(echo '${ENVIRONMENT_CONFIG_JSON}' | jq -r '.cloud.resourceGroup // .environmentName')

	for cluster in $$(echo '${ENVIRONMENT_CONFIG_JSON}' | jq -c '.cloud.compute.clusters | .[]'); do
		if [[ "$$(echo "$$cluster" | jq -r '.apiHost')" == "true" ]]; then
			cluster_name=$$(echo "$$cluster" | jq -r '.name')
			az account set --subscription "$${subscription}"
			az aks get-credentials -n "$${cluster_name}" -g "$${resource_group}" --overwrite-existing 
			kubelogin convert-kubeconfig -l azurecli
			kubectl config set-context --current --namespace=${HELM_NAMESPACE}
		fi
	done

set-localsettings:
	if [[ $$(helm list -n "${HELM_NAMESPACE}" -l name=${HELM_RELEASE} -o json | jq length) == 0 ]]; then
		echo "Run 'make up' before this target"; exit 1
	fi

	buffer_secret_name=$$(echo '${ENVIRONMENT_CONFIG_JSON}' | jq -r '.organizations["${DEFAULT_ORGANIATION}"].storage.buffers[0].name')
	buffer_secret_value="$$(kubectl get secrets -n ${HELM_NAMESPACE} $${buffer_secret_name} -o jsonpath="{.data.connectionString}" | base64 -d)"

	logs_secret_name=$$(echo '${ENVIRONMENT_CONFIG_JSON}' | jq -r '.organizations["${DEFAULT_ORGANIATION}"].storage.logs.name')
	logs_secret_value="$$(kubectl get secrets -n ${HELM_NAMESPACE} $${logs_secret_name} -o jsonpath="{.data.connectionString}" | base64 -d)"

	postgres_password="$$(kubectl get secrets -n ${HELM_NAMESPACE} ${HELM_RELEASE}-db -o jsonpath="{.data.postgresql-password}" | base64 -d)"

	buffer_sidecar_image="$$(docker inspect eminence.azurecr.io/buffer-sidecar:dev | jq -r --arg repo eminence.azurecr.io/buffer-sidecar '.[0].RepoDigests[] | select (startswith($$repo))')"
	worker_waiter_image="$$(docker inspect eminence.azurecr.io/worker-waiter:dev | jq -r --arg repo eminence.azurecr.io/worker-waiter '.[0].RepoDigests[] | select (startswith($$repo))')"

	jq <<- EOF > ${SERVER_PATH}/appsettings.local.json
		{
			"logging": { "Console": {"FormatterName": "simple" } },
			"auth": {
				"enabled": "${SECURITY_ENABLED}",
				"authority":"$$(echo '${ENVIRONMENT_CONFIG_JSON}' | jq -r '.organizations["${DEFAULT_ORGANIATION}"].authority')",
				"audience":"$$(echo '${ENVIRONMENT_CONFIG_JSON}' | jq -r '.organizations["${DEFAULT_ORGANIATION}"].audience')"
			},
			"kubernetes": {
				"kubeconfigPath": "$${HOME}/.kube/config",
				"namespace": "${HELM_NAMESPACE}",
				"jobServiceAccount": "${HELM_RELEASE}-job",
				"noOpConfigMap": "${HELM_RELEASE}-no-op",
				"workerWaiterImage": "$${worker_waiter_image}",
				"clusters": $$(echo '${ENVIRONMENT_CONFIG_JSON}' | jq -c '.clusters')
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
	registry=$$(scripts/get-context-environment-config.sh -e developerConfig.containerRegistry)
	scripts/build-images.sh --push --push-force --tag dev --quiet --registry "$${registry}"

docker-build-test:
	registry=$$(scripts/get-context-environment-config.sh -e developerConfig.containerRegistry)
	scripts/build-images.sh --test --push --push-force --tag test --quiet --registry "$${registry}"

publish-cli-tools:
	./scripts/publish-binaries.sh --push --use-git-hash-as-tag

up: install-cloud-conditionally docker-build install-cli-tyger-only
	repo_fqdn=$$(scripts/get-context-environment-config.sh -e developerConfig.containerRegistryFQDN)

	tyger_server_image="$$(docker inspect "$${repo_fqdn}/tyger-server:dev" | jq -r --arg repo "$${repo_fqdn}/tyger-server" '.[0].RepoDigests[] | select (startswith($$repo))')"
	buffer_sidecar_image="$$(docker inspect "$${repo_fqdn}/buffer-sidecar:dev" | jq -r --arg repo "$${repo_fqdn}/buffer-sidecar" '.[0].RepoDigests[] | select (startswith($$repo))')"
	worker_waiter_image="$$(docker inspect "$${repo_fqdn}/worker-waiter:dev" | jq -r --arg repo "$${repo_fqdn}/worker-waiter" '.[0].RepoDigests[] | select (startswith($$repo))')"

	chart_dir=$$(readlink -f deploy/helm/tyger)
	
	tyger install api -f <(scripts/get-context-environment-config.sh) \
		--set api.helm.tyger.chartRef="$${chart_dir}" \
		--set api.helm.tyger.values.server.image="$${tyger_server_image}" \
		--set api.helm.tyger.values.server.bufferSidecarImage="$${buffer_sidecar_image}" \
		--set api.helm.tyger.values.server.workerWaiterImage="$${worker_waiter_image}"

down: install-cli-tyger-only
	tyger uninstall api -f <(scripts/get-context-environment-config.sh)

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

test: unit-test integration-test e2e

full:
	$(MAKE) test SHOULD_INSTALL_CLOUD=true

test-no-up: unit-test integration-test-no-up e2e-no-up

forward: set-context
	scripts/forward-services.sh -n ${HELM_NAMESPACE}

check-forwarding:
	if ! curl "http://${HELM_RELEASE}-server:8080/healthcheck" &> /dev/null ; then
		echo "run 'make forward' in another terminal before running this target"
		exit 1
	fi

download-test-client-cert:
	if [[ ! -f ${TEST_CLIENT_CERT_FILE} ]]; then
		rm -f ${TEST_CLIENT_CERT_FILE}
		subscription=$$(echo '${ENVIRONMENT_CONFIG_JSON}' | yq '.cloud.subscriptionId')
		az keyvault secret download --vault-name eminence --name tyger-test-client-cert --version ${TEST_CLIENT_CERT_VERSION} --file ${TEST_CLIENT_CERT_FILE} --subscription $${subscription}
		chmod 600 ${TEST_CLIENT_CERT_FILE}
	fi

check-test-client-cert:
	[ -f ${TEST_CLIENT_CERT_FILE} ]

get-tyger-uri:
	echo ${TYGER_URI}

login-service-principal: install-cli download-test-client-cert
	tyger login -f <(cat <<EOF
	serverUri: ${TYGER_URI}
	servicePrincipal: ${TEST_CLIENT_IDENTIFIER_URI}
	certificatePath: ${TEST_CLIENT_CERT_FILE}
	EOF
	)

start-proxy: install-cli download-test-client-cert
	tyger-proxy start -f <(cat <<EOF
	serverUri: ${TYGER_URI}
	servicePrincipal: ${TEST_CLIENT_IDENTIFIER_URI}
	certificatePath: ${TEST_CLIENT_CERT_FILE}
	allowedClientCIDRs: $$(hostname -i | awk '{print $$1"/32"}' | jq -c -R --slurp 'split("\n")[:-1]')
	logPath: "/tmp/tyger-proxy"
	EOF
	)

kill-proxy:
	killall tyger-proxy

login: install-cli download-test-client-cert
	tyger login "${TYGER_URI}"	

install-cli-tyger-only:
	cd cli
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go install -ldflags="-s -w" ./cmd/tyger

install-cli: install-cli-tyger-only
	cd cli
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go install -ldflags="-s -w" ./cmd/buffer-sidecar
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go install -ldflags="-s -w" ./cmd/tyger-proxy

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
