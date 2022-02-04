.ONESHELL:
SHELL = /bin/bash
.SHELLFLAGS = -ecuo pipefail

SERVER_PATH=server/tyger.server

SECURITY_ENABLED=true
AUTHORITY=https://login.microsoftonline.com/76d3279b-830e-4bea-baf8-12863cdeba4c/
AUDIENCE=api://tyger-server
HELM_NAMESPACE=tyger
HELM_RELEASE=tyger
HELM_CHART_DIR=./deploy/helm/tyger
TYGER_SERVER_HOSTNAME=tyger.localdev.me
AZURITE_HOSTNAME=devstoreaccount1.azurite.localdev.me
TEST_CLIENT_CERT_FILE=~/tyger_test_client_cert.pem
TEST_CLIENT_IDENTIFIER_URI=api://tyger-test-client
AZURE_SUBSCRIPTION=BiomedicalImaging-NonProd

.SILENT: set-ini run docker-build up get-namespace unit-test check-forwarding

set-ini:
	if [[ $$(helm list -n "${HELM_NAMESPACE}" -l name=${HELM_RELEASE} -o json | jq length) == 0 ]]; then
		echo "Run 'make up' before this target"; exit 1
	fi

	postgres_password=$(shell kubectl get secrets ${HELM_RELEASE}-db -o jsonpath="{.data.postgresql-password}" | base64 -d)
	cat <<- EOF > ${SERVER_PATH}/localsettings.ini
		[Logging:Console]
		FormatterName=simple
		 
		[Auth]
		Enabled=${SECURITY_ENABLED}
		Authority=${AUTHORITY}
		Audience=${AUDIENCE}
		 
		[Kubernetes]
		KubeconfigPath=$${HOME}/.kube/config
		Namespace=${HELM_NAMESPACE}
		 
		[BlobStorage]
		AccountEndpoint=http://devstoreaccount1.${HELM_NAMESPACE}:10000/
		EmulatorExternalEndpoint=http://${AZURITE_HOSTNAME}
		 
		[Database]
		ConnectionString="Host=tyger-db; Database=tyger; Port=5432; Username=postgres; Password=$${postgres_password}"
		 
		[StorageServer]
		Uri="http://${HELM_RELEASE}-storage.${HELM_NAMESPACE}:8080"
	EOF

build:
	cd ${SERVER_PATH}
	dotnet build

run: set-ini check-forwarding
	if ! ping -c 1 "${HELM_RELEASE}-db" &> /dev/null ; then
		echo "run 'make forward' in another terminal before running this target"
		exit 1
	fi
	cd ${SERVER_PATH}
	dotnet run -v m

watch: set-ini check-forwarding
	cd ${SERVER_PATH}
	dotnet watch

unit-test:
	echo "Running unit tests..."
	cd cli
	go test ./... | { grep -v "\\[[no test files\\]" || true; }
	cd ../server/tyger.server.unittests
	dotnet test

docker-build:
	scripts/build-images.sh

up: docker-build
	tyger_server_image_id=$$(docker inspect tyger-server | jq -r '.[0].Id')

	echo "Applying Helm chart..."
	helm upgrade --install \
				--create-namespace -n "${HELM_NAMESPACE}" \
				"${HELM_RELEASE}" \
				"${HELM_CHART_DIR}" \
				--set server.image="$${tyger_server_image_id}" \
				--set server.security.enabled=true \
				--set server.security.authority="${AUTHORITY}" \
				--set server.security.audience="${AUDIENCE}" \
				--set server.hostname="${TYGER_SERVER_HOSTNAME}" \
				--set storageEmulator.enabled=true \
				--set storageEmulator.hostname="${AZURITE_HOSTNAME}" \
				--atomic

	echo "Waiting for successful health check..."
	timeout 30s bash -c 'while [[ "$$(curl -s -o /dev/null -m 1 -w ''%{http_code}'' tyger.localdev.me/healthcheck)" != "200" ]]; do sleep 0.1; done'
	echo "Ready"

down:
	kubectl delete secret -n "${HELM_NAMESPACE}" -l tyger=run
	kubectl delete pod -n "${HELM_NAMESPACE}" -l tyger=run
	if [[ $$(helm list -n "${HELM_NAMESPACE}" -l name=${HELM_RELEASE} -o json | jq length) != 0 ]]; then
		helm delete -n "${HELM_NAMESPACE}" ${HELM_RELEASE}
	fi

	kubectl delete pvc -n "${HELM_NAMESPACE}" -l app.kubernetes.io/instance=tyger

install-cli:
	cd cli
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go install -ldflags="-s -w" -v ./cmd/tyger

e2e-no-up:
	cd cli/test/e2e
	go test -tags=e2e

e2e: up e2e-no-up

test: unit-test e2e

test-no-up: unit-test e2e-no-up

get-namespace:
	echo ${HELM_NAMESPACE}

forward:
	scripts/forward-services.sh -n ${HELM_NAMESPACE}

check-forwarding:
	if ! ping -c 1 "${HELM_RELEASE}-db" &> /dev/null ; then
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
	tyger login http://${TYGER_SERVER_HOSTNAME} --service-principal ${TEST_CLIENT_IDENTIFIER_URI} --cert ${TEST_CLIENT_CERT_FILE}

connect-db:
	postgres_password=$(shell kubectl get secrets ${HELM_RELEASE}-db -o jsonpath="{.data.postgresql-password}" | base64 -d)
	cmd="PGPASSWORD=$${postgres_password} psql -d tyger -U postgres"
	kubectl exec ${HELM_RELEASE}-db-0 -it -- bash -c "$${cmd}"

restore:
	cd cli
	go mod download
	cd ..
	find server -name *csproj | xargs -L 1 dotnet restore
