environmentName: {{ .EnvironmentName }}

cloud:
  tenantId: {{ .TenantId }}
  subscriptionId: {{ .SubscriptionId }}
  resourceGroup: {{ .ResourceGroup }}
  defaultLocation: {{ .DefaultLocation}}

  compute:
    clusters:
      - name: {{ .EnvironmentName }}
        apiHost: true
        kubernetesVersion: {{ .KubernetesVersion }}
        # location: Defaults to defaultLocation

        userNodePools:
          - name: cpunp
            vmSize: Standard_DS12_v2
            minCount: 0
            maxCount: 10
          - name: gpunp
            vmSize: Standard_NC6s_v3
            minCount: 0
            maxCount: 10

    # These are the principals that will be granted full access to the
    # "tyger" namespace in each cluster.
    # For users, kind must be "User".
    #   If the user's home tenant is this subsciption's tenant and is not a personal Microsoft account,
    #   set id to the user principal name (email). Otherwise, set id to the object ID (GUID).
    # For service principals, kind must also be "User" and id must be the service principal's object ID (GUID).
    # For groups, kind must be "Group" and id must be the group's object ID (GUID).
    managementPrincipals:
      - kind: {{ .PrincipalKind }}
        id: {{ .PrincipalId }} {{- if not (contains .PrincipalId "@") }} # {{ .PrincipalDisplay }} {{- end }}

    # Optionally point an existing Log Analytics workspace to send logs to.
    # logAnalyticsWorkspace:
    #   resourceGroup:
    #   name:

    # The names of private container registries that the clusters must be able to pull from
    # privateContainerRegistries:
    #   - myprivateregistry

  database:
    serverName: {{ .DatabaseServerName }}
    postgresMajorVersion: {{ .PostgresMajorVersion }}
    # location: Defaults to defaultLocation
    # computeTier: Defaults to Burstable
    # vmSize: Defaults to Standard_B1ms
    # initialDatabaseSize: Defaults to 32GB (the minimum supported)
    # backupRetentionDays: Defaults to 7
    # backupGeoRedundancy: Defaults to false

  storage:
    # Storage accounts for buffers.
    buffers:
      - name: {{ .BufferStorageAccountName }}
        # location: Defaults to defaultLocation
        # sku: Defaults to Standard_LRS

    # The storage account where run logs will be stored.
    logs:
      name: {{ .LogsStorageAccountName }}
      # location: Defaults to defaultLocation
      # sku: Defaults to Standard_LRS

api:
  # The fully qualified domain name for the Tyger API.
  domainName: {{ .DomainName }}

  auth:
    tenantId: {{ .ApiTenantId }}
    apiAppUri: api://tyger-server
    cliAppUri: api://tyger-cli

  # Optional Helm chart overrides
  # helm:
  #   tyger:
  #     repoName:
  #     repoUrl:
  #     chartRef:
  #     namespace:
  #     version:
  #     values:

  #   certManager: {}
  #   nvidiaDevicePlugin: {}
  #   traefik: {}
