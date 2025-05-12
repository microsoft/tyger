#! /usr/bin/env bash

set -euo pipefail

usage() {
    cat <<EOF

managed-federated-creds.sh {--add-this | --remove-others}

Use this script with --add-this to add this VM's managed identity to the federated credentials
of the api://tyger-client-owner and api://tyger-client-contributor applications.

A maximum of 20 credentials are permitted per application, so you can remove credentials that
you created for other VMs by using the --remove-others option. This will only remove credentials
that were created by you.

EOF
}

while [[ $# -gt 0 ]]; do
    key="$1"
    case $key in
    --add-this)
        add=true
        shift
        ;;
    --remove-others)
        remove=true
        shift
        ;;
    -h | --help)
        usage
        exit
        ;;
    *)
        echo "ERROR: unknown option \"$key\""
        usage
        exit 1
        ;;
    esac
done

if [[ -z "${add:-}" && -z "${remove:-}" ]]; then
    echo "ERROR: you must specify either --add-this or --remove-others"
    exit 1
fi

if [[ ! "$(git config user.email)" =~ [^@]+ ]]; then
    echo >&2 "Eensure your git email is set"
    exit 1
fi

username="${BASH_REMATCH[0]//[.\-_]/}"

target_app_uris=(
    "api://tyger-client-owner"
    "api://tyger-client-contributor"
)

tenant_id=$("$(dirname "$0")/get-config.sh" --output json | jq -r '.cloud.tenantId')

vm_metadata=$(curl -s --fail -H Metadata:true --noproxy "*" "http://169.254.169.254/metadata/instance?api-version=2021-02-01" --connect-timeout 2)
mi_client_id=$(curl 'http://169.254.169.254/metadata/identity/oauth2/token?api-version=2018-02-01&resource=https%3A%2F%2Fmanagement.azure.com%2F' -H Metadata:true -s | jq -r '.client_id')
mi_object_id=$(az ad sp show --id "$mi_client_id" --query id -o tsv)

vm_name=$(echo "$vm_metadata" | jq -r '.compute.name')
vm_resource_group=$(echo "$vm_metadata" | jq -r '.compute.resourceGroupName')
vm_subscription_id=$(echo "$vm_metadata" | jq -r '.compute.subscriptionId')

desired_credential_name="${username}_${vm_subscription_id}_${vm_resource_group}_${vm_name}"

for target_app_uri in "${target_app_uris[@]}"; do
    if [[ "${add:-}" == true ]]; then
        # Check if the credential already exists
        existing_credential=$(az ad app federated-credential list --id "$target_app_uri" --query "[?name=='$desired_credential_name']" -o tsv)

        if [[ -n "$existing_credential" ]]; then
            continue
        fi

        az ad app federated-credential create --id "$target_app_uri" \
            --parameters "{
                \"name\": \"$desired_credential_name\",
                \"issuer\": \"https://login.microsoftonline.com/${tenant_id}/v2.0\",
                \"subject\": \"${mi_object_id}\",
                \"audiences\": [\"api://AzureADTokenExchange\"]
            }"
    else
        existing_credentials=$(az ad app federated-credential list --id "$target_app_uri")
        for credential in $(echo "$existing_credentials" | jq -c '.[]'); do
            credential_name=$(echo "$credential" | jq -r '.name')
            # Assuming no underscope in alias
            if [[ "$credential_name" != "${username}_"* ]]; then
                continue
            fi

            if [[ "$credential_name" == "$desired_credential_name" ]]; then
                continue
            fi

            credential_id=$(echo "$credential" | jq -r '.id')
            az ad app federated-credential delete --id "$target_app_uri" --federated-credential-id "$credential_id"
        done

    fi

done
