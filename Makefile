.ONESHELL:
SHELL = /bin/bash
.SHELLFLAGS = -ecuo pipefail

HELM_NAMESPACE=tyger
HELM_RELEASE=tyger
HELM_CHART_DIR=./deploy/helm/tyger
TYGER_SERVER_HOSTNAME=tyger.localdev.me
AZURITE_HOSTNAME=devstoreaccount1.azurite.localdev.me

.SILENT: set-env run docker-build up get-namespace unit-test

generate:
	go generate ./...
	go generate -tags=e2e ./...

build:
	go build -o bin/server ./cmd/server/

set-env:
	if [[ $$(helm list -n "${HELM_NAMESPACE}" -l name=${HELM_RELEASE} -o json | jq length) == 0 ]]; then
		echo "Run 'make up' before this target"; exit 1
	fi

	postgres_password=$(shell kubectl get secrets ${HELM_RELEASE}-db -o jsonpath="{.data.postgresql-password}" | base64 -d)
	cat <<- EOF > .env
		TYGER_KUBECONFIG_PATH=$${HOME}/.kube/config
		TYGER_STORAGE_ACCOUNT_CONNECTION_STRING="DefaultEndpointsProtocol=http;AccountName=devstoreaccount1;AccountKey=Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw==;BlobEndpoint=http://devstoreaccount1.${HELM_NAMESPACE}:10000/"
		TYGER_STORAGE_EMULATOR_EXTERNAL_HOST=${AZURITE_HOSTNAME}
		TYGER_KUBERNETES_NAMESPACE=${HELM_NAMESPACE}
		TYGER_DATABASE_CONNECTION_STRING="host=tyger-db dbname=tyger port=5432 user=postgres password=$${postgres_password}"
		TYGER_MRD_STORAGE_URI="http://${HELM_RELEASE}-storage.${HELM_NAMESPACE}:8080"
	EOF

run: set-env
	go run ./cmd/server -p

unit-test:
	echo "Running unit tests..."
	go test ./... | grep -v "\\[[no test files\\]"

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
	go install ./cmd/tyger

e2e-no-up: install-cli
	cd test/e2e
	go test -tags=e2e

e2e: up e2e-no-up

test: unit-test e2e

get-namespace:
	echo ${HELM_NAMESPACE}

forward:
	scripts/forward-services.sh -n ${HELM_NAMESPACE}
