.ONESHELL:
SHELL = /bin/bash
.SHELLFLAGS = -ecuo pipefail

.DEFAULT_GOAL := test

# trick to lazily evaluate this at most once: https://make.mad-scientist.net/deferred-simple-variable-expansion/
ENVIRONMENT_CONFIG = $(eval ENVIRONMENT_CONFIG := $$(shell scripts/get-context-environment-config.sh))$(if $(ENVIRONMENT_CONFIG),$(ENVIRONMENT_CONFIG),$(error "get-context-environment-config.sh failed"))

SERVER_PATH=server/Tyger.Server
SECURITY_ENABLED=true
DEFAULT_ORGANIATION=lamna
HELM_NAMESPACE=lamna
HELM_RELEASE=tyger
TEST_CLIENT_CERT_VERSION=1db664a6a3c74b6f817f3d842424003d
TEST_CLIENT_CERT_FILE=~/tyger_test_client_cert_${TEST_CLIENT_CERT_VERSION}.pem
TEST_CLIENT_IDENTIFIER_URI=api://tyger-test-client
AZURE_SUBSCRIPTION=BiomedicalImaging-NonProd
TYGER_URI = https://$(shell echo '${ENVIRONMENT_CONFIG}' | jq -r '.organizations["${DEFAULT_ORGANIATION}"].subdomain').$(shell echo '${ENVIRONMENT_CONFIG}' | jq -r '.dependencies.dnsZone.name')

ensure-environment:
	echo '${ENVIRONMENT_CONFIG}' | deploy/scripts/environment/ensure-environment.sh -c -

remove-environment: down
	echo '${ENVIRONMENT_CONFIG}' | deploy/scripts/environment/remove-environment.sh -c -

set-context:
	subscription=$$(echo '${ENVIRONMENT_CONFIG}' | jq -r '.subscription')
	resource_group=$$(echo '${ENVIRONMENT_CONFIG}' | jq -r '.resourceGroup')
	primary_cluster_name=$$(echo '${ENVIRONMENT_CONFIG}' | jq -r '.primaryCluster')

	az account set --subscription "$${subscription}"

	az aks get-credentials -n "$${primary_cluster_name}" -g "$${resource_group}" --overwrite-existing
	kubectl config set-context --current --namespace=${HELM_NAMESPACE}

set-localsettings:
	if [[ $$(helm list -n "${HELM_NAMESPACE}" -l name=${HELM_RELEASE} -o json | jq length) == 0 ]]; then
		echo "Run 'make up' before this target"; exit 1
	fi

	buffer_secret_name=$$(echo '${ENVIRONMENT_CONFIG}' | jq -r '.organizations["${DEFAULT_ORGANIATION}"].storage.buffers[0].name')
	buffer_secret_value="$$(kubectl get secrets -n ${HELM_NAMESPACE} $${buffer_secret_name} -o jsonpath="{.data.connectionString}" | base64 -d)"

	logs_secret_name=$$(echo '${ENVIRONMENT_CONFIG}' | jq -r '.organizations["${DEFAULT_ORGANIATION}"].storage.logs.name')
	logs_secret_value="$$(kubectl get secrets -n ${HELM_NAMESPACE} $${logs_secret_name} -o jsonpath="{.data.connectionString}" | base64 -d)"

	postgres_password="$$(kubectl get secrets -n ${HELM_NAMESPACE} ${HELM_RELEASE}-db -o jsonpath="{.data.postgresql-password}" | base64 -d)"

	buffer_proxy_image="$$(docker inspect eminence.azurecr.io/buffer-proxy:dev | jq -r --arg repo eminence.azurecr.io/buffer-proxy '.[0].RepoDigests[] | select (startswith($$repo))')"
	worker_waiter_image="$$(docker inspect eminence.azurecr.io/worker-waiter:dev | jq -r --arg repo eminence.azurecr.io/worker-waiter '.[0].RepoDigests[] | select (startswith($$repo))')"

	jq <<- EOF > ${SERVER_PATH}/appsettings.local.json
		{
			"logging": { "Console": {"FormatterName": "simple" } },
			"auth": {
				"enabled": "${SECURITY_ENABLED}",
				"authority":"$$(echo '${ENVIRONMENT_CONFIG}' | jq -r '.organizations["${DEFAULT_ORGANIATION}"].authority')",
				"audience":"$$(echo '${ENVIRONMENT_CONFIG}' | jq -r '.organizations["${DEFAULT_ORGANIATION}"].audience')"
			},
			"kubernetes": {
				"kubeconfigPath": "$${HOME}/.kube/config",
				"namespace": "${HELM_NAMESPACE}",
				"jobServiceAccount": "${HELM_RELEASE}-job",
				"noOpConfigMap": "${HELM_RELEASE}-no-op",
				"workerWaiterImage": "$${worker_waiter_image}",
				"clusters": $$(echo '${ENVIRONMENT_CONFIG}' | jq -c '.clusters')
			},
			"logArchive": {
				"storageAccountConnectionString": "$${logs_secret_value}"
			},
			"buffers": {
				"connectionString": "$${buffer_secret_value}",
				"bufferProxyImage": "$${buffer_proxy_image}"
			},
			"database": {
				"connectionString": "Host=tyger-db; Database=tyger; Port=5432; Username=postgres; Password=$${postgres_password}"
			},
			"storageServer": {
				"Uri": "http://${HELM_RELEASE}-storage.${HELM_NAMESPACE}:8080"
			}

		}
	EOF

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
	find server -name *csproj | xargs -L 1 dotnet test --no-restore -v q
	
	cd cli
	go test ./... | { grep -v "\\[[no test files\\]" || true; }

docker-build:
	echo '${ENVIRONMENT_CONFIG}' | scripts/build-images.sh -c - --push --push-force --tag dev --quiet

docker-build-test:
	echo '${ENVIRONMENT_CONFIG}' | scripts/build-images.sh -c - --test --push --push-force --tag test --quiet

publish-cli-tools-image:
	echo '${ENVIRONMENT_CONFIG}' | scripts/build-images.sh -c - --cli-tools --push --use-git-hash-as-tag --quiet

up: ensure-environment docker-build
	echo '${ENVIRONMENT_CONFIG}' | deploy/scripts/tyger/tyger-up.sh -c -

down:
	echo '${ENVIRONMENT_CONFIG}' | deploy/scripts/tyger/tyger-down.sh -c -

buffer-proxy-integration-test: cli-ready
	pushd cli/integrationtest/buffer-proxy
	go test -tags=integrationtest 

integration-test-no-up-prereqs: docker-build-test

integration-test-no-up: integration-test-no-up-prereqs buffer-proxy-integration-test
	if ! echo '${ENVIRONMENT_CONFIG}' | timeout --foreground 30m scripts/wait-for-cluster-to-scale.sh -c -; then
		echo "timed out waiting for nodepools to scale"
		exit 1
	fi

	pushd cli/integrationtest/tyger
	go test -tags=integrationtest

integration-test: up integration-test-no-up-prereqs
	$(MAKE) -o integration-test-no-up-prereqs integration-test-no-up

e2e-no-up-prereqs: e2e-data
	
e2e-no-up: e2e-no-up-prereqs cli-ready
	pytest e2e --workers 100

e2e: up e2e-no-up-prereqs
	$(MAKE) -o e2e-no-up-prereqs e2e-no-up

dvc-data:
	scripts/check-login.sh
	dvc pull

gadgetron-data:
	python3 e2e/gadgetron/get_cases.py

e2e-data: dvc-data gadgetron-data

test: unit-test integration-test e2e

test-no-up: unit-test integration-test-no-up e2e-no-up

forward: set-context
	scripts/forward-services.sh -n ${HELM_NAMESPACE}

check-forwarding:
	if ! curl "http://${HELM_RELEASE}-storage:8080/healthcheck" &> /dev/null ; then
		echo "run 'make forward' in another terminal before running this target"
		exit 1
	fi

download-test-client-cert:
	if [[ ! -f ${TEST_CLIENT_CERT_FILE} ]]; then
		rm -f ${TEST_CLIENT_CERT_FILE}
		az keyvault secret download --vault-name eminence --name tyger-test-client-cert --version ${TEST_CLIENT_CERT_VERSION} --file ${TEST_CLIENT_CERT_FILE} --subscription ${AZURE_SUBSCRIPTION}
		chmod 600 ${TEST_CLIENT_CERT_FILE}
	fi

check-test-client-cert:
	[ -f ${TEST_CLIENT_CERT_FILE} ]

get-tyger-uri:
	echo ${TYGER_URI}

login-service-principal: install-cli download-test-client-cert
	tyger login '${TYGER_URI}' --service-principal ${TEST_CLIENT_IDENTIFIER_URI} --cert ${TEST_CLIENT_CERT_FILE}

login: install-cli download-test-client-cert
	tyger login "${TYGER_URI}"	

install-cli:
	cd cli
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go install -ldflags="-s -w" ./cmd/tyger
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go install -ldflags="-s -w" ./cmd/buffer-proxy

cli-ready: install-cli
	if ! tyger login status &> /dev/null; then
		$(MAKE) login-service-principal
	fi

connect-db:
	postgres_password=$(shell kubectl get secrets ${HELM_RELEASE}-db -o jsonpath="{.data.postgresql-password}" | base64 -d)
	cmd="PGPASSWORD=$${postgres_password} psql -d tyger -U postgres"
	kubectl exec ${HELM_RELEASE}-db-0 -it -- bash -c "$${cmd}"

restore:
	cd cli
	go mod download
	cd ..
	find server -name *csproj | xargs -L 1 dotnet restore

format:
	find server -name *csproj | xargs -L 1 dotnet format

verify-format:
	find server -name *csproj | xargs -i sh -c 'dotnet build -p:EnforceCodeStyleInBuild=true {} && dotnet format --verify-no-changes {}'

purge-runs: set-context
	for pod in $$(kubectl get pod -n "${HELM_NAMESPACE}" -l tyger-run -o go-template='{{range .items}}{{.metadata.name}}{{"\n"}}{{end}}'); do
		kubectl patch pod -n "${HELM_NAMESPACE}" "$${pod}" \
			--type json \
			--patch='[ { "op": "remove", "path": "/metadata/finalizers" } ]'
	done
	kubectl delete job,statefulset,secret,service -n "${HELM_NAMESPACE}" -l tyger-run --cascade=foreground
