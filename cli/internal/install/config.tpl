environmentName: {{ .EnvironmentName }}

cloud:
  tenantId: {{ .TenantId }}
  subscriptionId: {{ .SubscriptionId }}
  defaultLocation: {{ .DefaultLocation}}
  # resourceGroup: # defaults to the environmentName

  compute:
    clusters:
      - name: {{ .EnvironmentName }}
        apiHost: true
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
    managementPrincipalIds:
      - objectId: {{ .CurrentUserId }} # {{ .CurrentUserDisplayName }}
        type: {{ .PrincipalKind }}

    # The names of private container registries that the clusters must be able to pull from
    # privateContainerRegistries:
    #   - myprivateregistry

  storage:
    buffers:
      - name: {{ .BufferStorageAccountName }}
        # location: Defaults to defaultLocation
        # sku: Defaults to Standard_LRS

    logs:
      name: {{ .LogsStorageAccountName }}
      # location: Defaults to defaultLocation
      # sku: Defaults to Standard_LRS

api:
  # The domain name for the Tyger API.
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
