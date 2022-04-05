.ONESHELL:
SHELL = /bin/bash
.SHELLFLAGS = -ecuo pipefail

SERVER_PATH=server/Tyger.Server

SECURITY_ENABLED=true
DEFAULT_ORGANIATION=lamna
HELM_NAMESPACE=lamna
HELM_RELEASE=tyger
TEST_CLIENT_CERT_FILE=~/tyger_test_client_cert.pem
TEST_CLIENT_IDENTIFIER_URI=api://tyger-test-client
AZURE_SUBSCRIPTION=BiomedicalImaging-NonProd

.SILENT: set-localsettings run docker-build up get-namespace unit-test check-forwarding

ensure-environment:
	scripts/get-context-environment-config.sh | deploy/scripts/ensure-environment.sh -c -

remove-environment:
	scripts/get-context-environment-config.sh | deploy/scripts/remove-environment.sh -c -

set-context:
	environment_config=$$(scripts/get-context-environment-config.sh)
	subscription=$$(echo "$${environment_config}" | jq -r '.subscription')
	resource_group=$$(echo "$${environment_config}" | jq -r '.resourceGroup')
	primary_cluster_name=$$(echo "$${environment_config}" | jq -r '.primaryCluster')

	az account set --subscription "$${subscription}"

	az aks get-credentials -n "$${primary_cluster_name}" -g "$${resource_group}" --overwrite-existing
	kubectl config set-context --current --namespace=${HELM_NAMESPACE}
	

set-localsettings:
	if [[ $$(helm list -n "${HELM_NAMESPACE}" -l name=${HELM_RELEASE} -o json | jq length) == 0 ]]; then
		echo "Run 'make up' before this target"; exit 1
	fi

	environment_config=$$(scripts/get-context-environment-config.sh)
	buffer_secret_name=$$(echo "$${environment_config}" | jq -r '.organizations["${DEFAULT_ORGANIATION}"].storage.buffers[0].name')
	buffer_secret_value="$$(kubectl get secrets -n ${HELM_NAMESPACE} $${buffer_secret_name} -o jsonpath="{.data.connectionString}" | base64 -d)"
	postgres_password="$$(kubectl get secrets -n ${HELM_NAMESPACE} ${HELM_RELEASE}-db -o jsonpath="{.data.postgresql-password}" | base64 -d)"
	jq <<- EOF > ${SERVER_PATH}/appsettings.local.json
		{
			"logging": { "Console": {"FormatterName": "simple" } },
			"auth": {
				"enabled": "${SECURITY_ENABLED}",
				"authority":"$$(echo "$${environment_config}" | jq -r '.organizations["${DEFAULT_ORGANIATION}"].authority')",
				"audience":"$$(echo "$${environment_config}" | jq -r '.organizations["${DEFAULT_ORGANIATION}"].audience')"
			},
			"kubernetes": {
				"kubeconfigPath": "$${HOME}/.kube/config",
				"namespace": "${HELM_NAMESPACE}",
				"clusters": $$(echo "$${environment_config}" | jq -c '.clusters')
			},
			"blobStorage": {
				"connectionString": "$${buffer_secret_value}"
			},
			"database": {
				"connectionString": "Host=tyger-db; Database=tyger; Port=5432; Username=postgres; Password=$${postgres_password}"
			},
			"storageServer": {
				"Uri": "http://${HELM_RELEASE}-storage.${HELM_NAMESPACE}:8080"
			}

		}
	EOF

build:
	cd ${SERVER_PATH}
	dotnet build

run: set-localsettings check-forwarding
	cd ${SERVER_PATH}
	dotnet run -v m

watch: set-localsettings check-forwarding
	cd ${SERVER_PATH}
	dotnet watch

unit-test:
	echo "Running unit tests..."
	find server -name *csproj | xargs -L 1 dotnet test
	
	cd cli
	go test ./... | { grep -v "\\[[no test files\\]" || true; }

docker-build:
	scripts/get-context-environment-config.sh | scripts/build-images.sh -c - --push --push-force --tag dev 

docker-build-test:
	scripts/get-context-environment-config.sh | scripts/build-images.sh -c - --test --push --push-force --tag test

up: docker-build
	scripts/get-context-environment-config.sh | deploy/scripts/tyger-up.sh -c -

down:
	scripts/get-context-environment-config.sh | deploy/scripts/tyger-down.sh -c -

install-cli:
	cd cli
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go install -ldflags="-s -w" -v ./cmd/tyger

e2e-no-up: docker-build-test
	cd cli/test/e2e
	go test -tags=e2e

e2e: up e2e-no-up

test: unit-test e2e

test-no-up: unit-test e2e-no-up

forward:
	scripts/forward-services.sh -n ${HELM_NAMESPACE}

check-forwarding:
	if ! curl "http://${HELM_RELEASE}-storage:8080/healthcheck" &> /dev/null ; then
		echo "run 'make forward' in another terminal before running this target"
		exit 1
	fi

download-test-client-cert:
	rm -f ${TEST_CLIENT_CERT_FILE}
	az keyvault secret download --vault-name eminence --name tyger-test-client-cert --file ${TEST_CLIENT_CERT_FILE} --subscription ${AZURE_SUBSCRIPTION}
	chmod 600 ${TEST_CLIENT_CERT_FILE}

check-test-client-cert:
	[ -f ${TEST_CLIENT_CERT_FILE} ]

login-service-principal:
	environment_config=$$(scripts/get-context-environment-config.sh)
	uri="https://$$(echo "$${environment_config}" | jq -r '.organizations["${DEFAULT_ORGANIATION}"].subdomain').$$(echo "$${environment_config}" | jq -r '.dependencies.dnsZone.name')"

	
	tyger login "$${uri}" --service-principal ${TEST_CLIENT_IDENTIFIER_URI} --cert ${TEST_CLIENT_CERT_FILE}

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
