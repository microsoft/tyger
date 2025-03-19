kind: azureCloud
environmentName: {{ .EnvironmentName }}

cloud:
  tenantId: {{ .TenantId }}
  subscriptionId: {{ .SubscriptionId }}
  resourceGroup: {{ .ResourceGroup }}
  defaultLocation: {{ .DefaultLocation}}

  # Optionally point an existing Log Analytics workspace to send logs to.
  # logAnalyticsWorkspace:
  #   resourceGroup:
  #   name:

  compute:
    clusters:
      - name: {{ .EnvironmentName }}
        apiHost: true
        kubernetesVersion: "{{ .KubernetesVersion }}"
        # location: Defaults to defaultLocation

        systemNodePool:
          name: system
          vmSize: Standard_DS2_v2
          minCount: 1
          maxCount: 3

        userNodePools:
          - name: cpunp
            vmSize: Standard_DS12_v2
            minCount: {{ .CpuNodePoolMinCount }}
            maxCount: 10
          - name: gpunp
            vmSize: Standard_NC6s_v3
            minCount: {{ .GpuNodePoolMinCount }}
            maxCount: 10

    # These are the principals that will have the ability to run `tyger api install`.
    # They will have access to the "tyger" namespace in each cluster and will have
    # the necessary Azure RBAC role assignments.
    # For users:
    #   "kind" must be set to "User"
    #   "objectId" must be set to the object ID GUID
    #   "userPrincipalName" must be set (this is usually the email address, unless this is a guest account)
    # For groups:
    #   "kind" must be set to "Group"
    #   "objectId" must be set to the object ID GUID
    # For service principals:
    #   "kind" must be set to "ServicePrincipal"
    #   "objectId" must be set to the object ID GUID
    managementPrincipals:
      - kind: {{ .Principal.Kind }}
        {{- if .Principal.UserPrincipalName }}
        userPrincipalName: {{ .Principal.UserPrincipalName }}
        {{- end }}
        objectId: {{ .Principal.ObjectId }}

    # The names of private container registries that the clusters must
    # be able to pull from.
    # privateContainerRegistries:
    #   - myprivateregistry

    # An optional array of managed identities that will be created in the resource group.
    # These identities are available to runs as workload identities. When updating this list
    # both `tyger cloud install` and `tyger api installed` must be run.
    # identities:
    # - my-identity

  database:
    serverName: {{ .DatabaseServerName }}
    postgresMajorVersion: {{ .PostgresMajorVersion }}

    # Firewall rules to control where the database can be accessed from,
    # in addition to the control-plane cluster.
    # firewallRules:
    #  - name:
    #    startIpAddress:
    #    endIpAddress:

    # location: Defaults to defaultLocation
    # computeTier: Defaults to Burstable
    # vmSize: Defaults to Standard_B1ms
    # storageSizeGB: Defaults to 32GB (the minimum supported)
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
  #     repoUrl: # not set if using `chartRef`
  #     chartRef: # e.g. oci://mcr.microsoft.com/tyger/helm/tyger
  #     namespace:
  #     version:
  #     values:

  #   certManager: {} # same fields as `tyger` above
  #   nvidiaDevicePlugin: {} # same fields as `tyger` above
  #   traefik: {} # same fields as `tyger` above
