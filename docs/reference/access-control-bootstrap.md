# Bootstrapping access control with an Entra ID admin

The [`tyger access-control apply`](../introduction/installation/cloud-installation.md#set-up-access-control)
command is a convenience that creates and maintains the two Microsoft Entra ID
app registrations that Tyger uses for authentication (one for the API server and
one for the CLI), and that keeps the user/group role assignments on them in
sync.

In some organizations, the people who will operate Tyger day-to-day do not have
the directory permissions required to create app registrations, and the Entra ID
admins who do have those permissions are not willing to run a third-party tool
such as `tyger` against the directory. This page describes how to do the initial
bootstrap using only `az` commands an Entra admin can review, after which the
Tyger operators ("owners") can take over and run `tyger access-control apply`
themselves for all subsequent changes.

## What this bootstrap does

The script below performs the minimum set of steps that require Entra admin
privileges:

1. Creates the API app registration and its service principal.
2. Creates the CLI app registration and its service principal.
3. Adds the designated Tyger owners as **app owners** on both registrations.

Once an account is listed as an app owner, it can update the application object
(define app roles, OAuth2 scopes, pre-authorized clients, redirect URIs, etc.)
and grant role assignments on the API app's service principal. Everything else
that `tyger access-control apply` normally does is then performed by the Tyger
owner under their own identity — no further Entra admin involvement is required.

## Step 1 — Collect the Tyger owners' object IDs

Designate at least one Tyger owner. These are the people who will subsequently
run `tyger access-control apply` to manage the app registrations and role
assignments.

Each owner must be an individual user account — Entra ID app owners cannot be
groups or service principals.

Each owner can find their own Entra ID object ID by running:

```bash
az ad user show --id "$(az account show --query user.name -o tsv)" --query id -o tsv
```

Collect the object IDs of all the intended owners and pass them to the Entra
admin.

## Step 2 — Have the Entra admin run one of the bootstrap scripts

The admin should run **exactly one** of the two scripts below. They both produce
the same end state — two app registrations, two service principals, and the
Tyger owners listed as app owners on both — and both print the values the
Tyger owner needs in Step 3. They differ only in the form of the
[application ID URI](https://learn.microsoft.com/entra/identity-platform/security-best-practices-for-app-registration#application-id-uri)
they set on each app:

- **Script A** uses `api://{tenantId}/tyger-server` and
  `api://{tenantId}/tyger-cli`. These URIs are more readable than the app-ID
  form and are accepted by Entra by default. If your tenant has a
  [verified domain](https://learn.microsoft.com/entra/identity/users/domains-manage)
  and you would prefer an even friendlier URI such as
  `api://tyger.contoso.com/tyger-server`, edit the `api_app_uri` and
  `cli_app_uri` variables before running the script.
- **Script B** uses `api://{appId}` — each app's own client ID — as the
  identifier URI. The URI is less meaningful to a human reader, but this form
  is always accepted regardless of tenant policy. Use this variant if Script A
  is rejected (some tenants restrict identifier URIs to verified-domain hosts
  only).

In both scripts, fill in:

- `owner_object_ids` — the object IDs collected in Step 1.
- `api_app_display_name` and `cli_app_display_name` — friendly names shown in
  the Entra admin portal.

Both scripts are idempotent in the sense that re-running them after a
successful bootstrap will produce errors for the already-existing resources
but will not corrupt anything. The admin only needs to run their chosen
script once.

### Script A — tenant-ID (or verified-domain) URI

```bash
#!/usr/bin/env bash
set -euo pipefail

# One or more object IDs of the Tyger owners. These accounts will be added as
# app owners on both app registrations so they can subsequently run
# `tyger access-control apply` themselves.
owner_object_ids=(
  "FILL-IN-OWNER-OBJECT-ID-1"
  # "FILL-IN-OWNER-OBJECT-ID-2"
)

# Identifier URIs. The default `api://{tenantId}/...` form is accepted in any
# tenant. If your tenant has a verified domain you may prefer something like
# `api://tyger.contoso.com/tyger-server` instead.
tenant_id="$(az account show --query tenantId -o tsv)"
api_app_uri="api://${tenant_id}/tyger-server"
cli_app_uri="api://${tenant_id}/tyger-cli"

api_app_display_name="Tyger API"
cli_app_display_name="Tyger CLI"

# --- API app ---------------------------------------------------------------
api_app_id="$(az ad app create \
  --display-name "$api_app_display_name" \
  --identifier-uris "$api_app_uri" \
  --requested-access-token-version 2 \
  --query appId -o tsv)"

az ad sp create --id "$api_app_id"

for owner_object_id in "${owner_object_ids[@]}"; do
  az ad app owner add --id "$api_app_id" --owner-object-id "$owner_object_id"
done

# --- CLI app ---------------------------------------------------------------
cli_app_id="$(az ad app create \
  --display-name "$cli_app_display_name" \
  --identifier-uris "$cli_app_uri" \
  --requested-access-token-version 2 \
  --query appId -o tsv)"

az ad sp create --id "$cli_app_id"

for owner_object_id in "${owner_object_ids[@]}"; do
  az ad app owner add --id "$cli_app_id" --owner-object-id "$owner_object_id"
done

echo
echo "Bootstrap complete."
echo "  tenantId:  ${tenant_id}"
echo "  apiAppUri: ${api_app_uri}"
echo "  cliAppUri: ${cli_app_uri}"
echo "  ownerObjectIds:"
for owner_object_id in "${owner_object_ids[@]}"; do
  echo "    - ${owner_object_id}"
done
```

### Script B — `api://{appId}` fallback

Use this variant only if Script A is rejected by tenant policy (typically with
an error such as *"Values of identifierUris property must use a verified
domain of the organization or its subdomain"*).

```bash
#!/usr/bin/env bash
set -euo pipefail

owner_object_ids=(
  "FILL-IN-OWNER-OBJECT-ID-1"
  # "FILL-IN-OWNER-OBJECT-ID-2"
)

api_app_display_name="Tyger API"
cli_app_display_name="Tyger CLI"

tenant_id="$(az account show --query tenantId -o tsv)"

# --- API app ---------------------------------------------------------------
api_app_id="$(az ad app create \
  --display-name "$api_app_display_name" \
  --requested-access-token-version 2 \
  --query appId -o tsv)"

api_app_uri="api://${api_app_id}"
az ad app update --id "$api_app_id" --identifier-uris "$api_app_uri"
az ad sp create --id "$api_app_id"

for owner_object_id in "${owner_object_ids[@]}"; do
  az ad app owner add --id "$api_app_id" --owner-object-id "$owner_object_id"
done

# --- CLI app ---------------------------------------------------------------
cli_app_id="$(az ad app create \
  --display-name "$cli_app_display_name" \
  --requested-access-token-version 2 \
  --query appId -o tsv)"

cli_app_uri="api://${cli_app_id}"
az ad app update --id "$cli_app_id" --identifier-uris "$cli_app_uri"
az ad sp create --id "$cli_app_id"

for owner_object_id in "${owner_object_ids[@]}"; do
  az ad app owner add --id "$cli_app_id" --owner-object-id "$owner_object_id"
done

echo
echo "Bootstrap complete."
echo "  tenantId:  ${tenant_id}"
echo "  apiAppUri: ${api_app_uri}"
echo "  cliAppUri: ${cli_app_uri}"
echo "  ownerObjectIds:"
for owner_object_id in "${owner_object_ids[@]}"; do
  echo "    - ${owner_object_id}"
done
```

### Reporting back

Whichever script was used, ask the admin to send back the values printed at
the end (`tenantId`, `apiAppUri`, `cliAppUri`, and the owner object IDs) along
with confirmation that the script completed successfully.

## Step 3 — Owner takes over with `tyger access-control apply`

Once the bootstrap is complete, one of the Tyger owners edits the main Tyger
[cloud configuration file](../introduction/installation/cloud-installation.md#generate-an-installation-configuration-file)
(`config.yml`) and fills in the `accessControl` section under the relevant
organization, at the path `organizations[*].api.accessControl`:

```yaml
api:
  accessControl:
    tenantId: 72f988bf-86f1-41af-91ab-2d7cd011db47   # reported by the admin
    apiAppUri: api://72f988bf-…/tyger-server          # reported by the admin
    cliAppUri: api://72f988bf-…/tyger-cli             # reported by the admin

    apiAppId: "" # `tyger access-control apply` will fill in this value
    cliAppId: "" # `tyger access-control apply` will fill in this value

    roleAssignments:
      owner:
        # At minimum, list the Tyger owners here so they can use Tyger themselves.
        # These are the owner object IDs reported by the admin.
        - kind: User
          objectId: 33333333-3333-3333-3333-333333333333

      contributor: []
```

The values to set are:

- `tenantId`, `apiAppUri`, `cliAppUri` — reported by the admin at the end of
  the bootstrap script.
- `apiAppId` / `cliAppId` — leave empty; `tyger access-control apply` will
  fill them in from the app registrations.
- `roleAssignments.owner` — list each Tyger owner using the `objectId` value
  reported by the admin. Using `objectId` avoids the ambiguity of the user
  principal name, which does not always match a user's email address.
- `roleAssignments.contributor` — list any additional users, groups, or
  service principals that should have the contributor role. See
  [Set up access control](../introduction/installation/cloud-installation.md#set-up-access-control)
  for the supported principal forms.

Then apply the configuration:

```bash
tyger access-control apply -f config.yml
```

(If the configuration file contains more than one organization, also pass
`--org <name>` to select the one to apply.)

This is the command that fills in the rest of the app registration details:
defining the `owner` and `contributor` app roles, declaring the OAuth2
permission scope, configuring the CLI app as a public client with a
`http://localhost` redirect URI, marking the CLI app as a pre-authorized
client of the API app, and creating the requested app role assignments on the
API app's service principal.

From this point on, the Tyger owners can re-run `tyger access-control apply`
themselves whenever role assignments need to change — no further Entra admin
involvement is required.

## What is *not* done by the bootstrap script

For transparency when reviewing the script with the Entra admin, note that the
script intentionally does **not**:

- Define any app roles, OAuth2 permission scopes, pre-authorized clients, or
  redirect URIs. Those are configured later by the owner via
  `tyger access-control apply`.
- Grant admin consent for any permissions. The CLI app only requests a
  delegated scope on the API app (which is in the same tenant and pre-authorized
  by `tyger access-control apply`), so admin consent is not required.
- Assign any users to Tyger roles. That is done later by the owner via
  `roleAssignments` in the configuration file.
