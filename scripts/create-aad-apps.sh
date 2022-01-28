#! /bin/bash
# Creates or updates the tyger-server and tyger-cli AAD apps. We use the Graph API to have more flexibility than the az CLI.

set -euo pipefail

tenant_id="76d3279b-830e-4bea-baf8-12863cdeba4c"

if [[ $(az account show | jq -r '.tenantId' || true) != "${tenant_id}" ]]; then
    echo "This script must be run in the context of the '${tenant_id}' tenant."
    echo "run az login --tenant ${tenant_id}" --allow-no-subscriptions
    exit 1
fi


get_app_by_identifier_uri() {
    az rest --url "https://graph.microsoft.com/beta/applications/?\$filter=identifierUris/any(c:c eq '$1')"
}

server_object_id=$(get_app_by_identifier_uri 'api://tyger-server' | jq -r '.value[] | .id')

server_spec=$(cat  <<EOF | jq -c
{
    "displayName": "tyger-server",
    "identifierUris": ["api://tyger-server"],
    "api": {
        "requestedAccessTokenVersion": 1,
        "oauth2PermissionScopes": [
          {
            "adminConsentDescription": "Allow the application to read and write to the Tyger server on behalf of the signed-in user",
            "adminConsentDisplayName": "Access Tyger",
            "id": "6291652f-fd9d-4a31-aa5f-87306c599bb6",
            "isEnabled": true,
            "type": "User",
            "userConsentDescription": "Allow the application to access the Tyger server on your behalf",
            "userConsentDisplayName": "Access Tyger",
            "value": "Read.Write"
          }
        ]
    },
    "signInAudience": "AzureADMyOrg"
}
EOF
)

if [[ -z "${server_object_id}" ]]; then
    az rest --method POST --body "${server_spec}" --url "https://graph.microsoft.com/beta/applications/"
else
    az rest --method PATCH --body "${server_spec}" --url "https://graph.microsoft.com/beta/applications/${server_object_id}"
fi

server_app_id=$(get_app_by_identifier_uri 'api://tyger-server' | jq -r '.value[] | .appId')

if [[ $(az rest --url "https://graph.microsoft.com/beta/servicePrincipals/?\$filter=appId eq '${server_app_id}'" | jq '.value | length') == 0 ]]; then
    az rest --method post --body "{\"appId\": \"${server_app_id}\"}" --url https://graph.microsoft.com/beta/servicePrincipals
fi

cli_object_id=$(get_app_by_identifier_uri 'api://tyger-cli' | jq -r '.value[] | .id')

cli_spec=$(cat  <<EOF | jq -c
{
    "displayName": "tyger-cli",
    "identifierUris": ["api://tyger-cli"],
    "requiredResourceAccess": [
        {
          "resourceAppId": "${server_app_id}",
          "resourceAccess": [
            {
              "id": "6291652f-fd9d-4a31-aa5f-87306c599bb6",
              "type": "Scope"
            }
          ]
        }
    ],
    "isFallbackPublicClient": true,
    "publicClient": {
        "redirectUris": [
            "http://localhost"
        ]
    },
    "signInAudience": "AzureADMyOrg"
}
EOF
)

if [[ -z "${cli_object_id}" ]]; then
    az rest --method POST --body "${cli_spec}" --url "https://graph.microsoft.com/beta/applications/"
else
    az rest --method PATCH --body "${cli_spec}" --url "https://graph.microsoft.com/beta/applications/${cli_object_id}"
fi

cli_app_id=$(get_app_by_identifier_uri 'api://tyger-cli' | jq -r '.value[] | .appId')

if [[ $(az rest --url "https://graph.microsoft.com/beta/servicePrincipals/?\$filter=appId eq '${cli_app_id}'" | jq '.value | length') == 0 ]]; then
    az rest --method post --body "{\"appId\": \"${cli_app_id}\"}" --url https://graph.microsoft.com/beta/servicePrincipals
fi
