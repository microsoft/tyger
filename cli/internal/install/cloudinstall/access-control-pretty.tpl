
tenantId: {{ if .TenantID -}} {{ .TenantID }} {{- else }} # Required. {{- end }}
apiAppUri: {{ if .ApiAppUri -}} {{ .ApiAppUri }} {{- else }}# Required. {{- end }}
cliAppUri: {{ if .CliAppUri -}} {{ .CliAppUri }} {{- else }}# Required. {{- end }}

apiAppId: {{ if .ApiAppId -}} {{ .ApiAppId }} {{- else }} # `tyger access-control apply` will fill in this value {{- end }}
cliAppId: {{ if .CliAppId -}} {{ .CliAppId }} {{- else }} # `tyger access-control apply` will fill in this value {{- end }}
{{- if .ServiceManagementReference }}

serviceManagementReference: {{ .ServiceManagementReference }}
{{- end }}

# Principals in role assignments are specified in the following ways:
#
# For users, specify the object ID and/or the user principal name.
#   - kind: User
#     objectId: <objectId>
#     userPrincipalName: <userPrincipalName>
#
# For groups, specify the object ID and/or the group display name.
#   - kind: Group
#     objectId: <objectId>
#     displayName: <displayName>
#
# For service principals, specify the object ID and/or the service principal display name.
#   - kind: ServicePrincipal
#     objectId: <objectId>
#     displayName: <displayName>

{{- if (or .RoleAssignments.Owner .RoleAssignments.Owner) | not  }}

# Example:
#
# roleAssignments:
#   owner:
#     - kind: User
#       objectId: c5e5d858-b954-4bef-b704-71b63e761f69
#       userPrincipalName: me@example.com
#
#     - kind: ServicePrincipal
#       objectId: 32b73951-e5eb-4a75-8479-23374021f46a
#       displayName: my-service-principal
#
#   contributor:
#     - kind: Group
#       objectId: f939c89c-94ed-46b9-8fbd-726cf747f231
#       displayName: my-group
{{- end }}

roleAssignments:
{{- if .RoleAssignments.Owner }}
  owner:
  {{- range .RoleAssignments.Owner }}
    - {{ .Principal | toYAML | indentAfterFirst 6 }}
  {{- end }}
{{- else }}
  owner: []
{{- end }}

{{- if .RoleAssignments.Contributor }}
  contributor:
  {{- range .RoleAssignments.Contributor }}
    - {{ .Principal | toYAML | indentAfterFirst 6 }}
  {{- end }}
{{- else }}
  contributor: []
{{- end }}
