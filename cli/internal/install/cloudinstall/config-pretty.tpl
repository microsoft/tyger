kind: azureCloud
environmentName: {{ .EnvironmentName }}

{{ with .Cloud -}}
cloud:
  tenantId: {{ .TenantID}}
  subscriptionId: {{ .SubscriptionID }}
  resourceGroup: {{ .ResourceGroup }}
  defaultLocation: {{ .DefaultLocation}}

  # Optionally point an existing Log Analytics workspace to send logs to.
  {{ if .LogAnalyticsWorkspace -}}
  logAnalyticsWorkspace:
    resourceGroup: {{ .LogAnalyticsWorkspace.ResourceGroup }}
    name: {{ .LogAnalyticsWorkspace.Name }}
  {{- else -}}
  # logAnalyticsWorkspace:
    # resourceGroup:
    # name:
  {{- end }}

  # Optionaly use a custom DNS zone
  {{ if .DnsZone -}}
  dnsZone:
    resourceGroup: {{ .DnsZone.ResourceGroup }}
    name: {{ .DnsZone.Name }}
  {{- else -}}
  # dnsZone
    # resourceGroup:
    # name:
  {{- end }}

  # Optionally provide a TLS certificate, if not using Let's Encrypt
  {{ if .TlsCertificate -}}
  tlsCertificate:
    keyVault:
      resourceGroup: {{ .TlsCertificate.KeyVault.ResourceGroup }}
      name: {{ .TlsCertificate.KeyVault.Name }}
    certificateName: {{ .TlsCertificate.CertificateName }}
  {{- else -}}
  # tlsCertificate:
    # keyVault:
    #   resourceGroup:
    #   name:
    # certificateName:
  {{- end }}

  {{ with .Compute -}}
  compute:
    clusters:
    {{- range .Clusters }}
      - name: {{ .Name }}
        apiHost: true
        kubernetesVersion: "{{ .KubernetesVersion }}"
        {{ optionalField "sku" .Sku "defaults to defaultLocation" }}
        {{ optionalField "location" .Location "defaults to Standard" }}
        systemNodePool:
          name: {{ .SystemNodePool.Name }}
          vmSize: {{ .SystemNodePool.VMSize }}
          minCount: {{ .SystemNodePool.MinCount }}
          maxCount: {{ .SystemNodePool.MaxCount }}
          {{ optionalField "osSku" .SystemNodePool.OsSku "defaults to AzureLinux" }}

        userNodePools:
          {{- range .UserNodePools }}
          - name: {{ .Name }}
            vmSize: {{ .VMSize }}
            minCount: {{ .MinCount }}
            maxCount: {{ .MaxCount }}
            {{- if .OsSku }}
            osSku: {{ .OsSku }}
            {{- else }}
            # osSku: defaults to AzureLinux
            {{- end }}
          {{- end }}
    {{- end }}

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
      {{- range .ManagementPrincipals }}
      - kind: {{ .Kind }}
        {{- if .UserPrincipalName }}
        userPrincipalName: {{ .UserPrincipalName }}
        {{- end }}
        objectId: {{ .ObjectId }}
      {{- end }}
    {{- if .LocalDevelopmentIdentityId }}

    localDevelopmentIdentityId: {{ .LocalDevelopmentIdentityId }}
    {{- end }}

    # The names of private container registries that the clusters must
    # be able to pull from.
    {{ if .PrivateContainerRegistries -}}
    privateContainerRegistries:
      {{- range .PrivateContainerRegistries }}
      - {{ . }}
      {{- end }}
    {{- else -}}
    # privateContainerRegistries:
    #   - myprivateregistry
    {{- end }}

    # This must be set if using a custom DNS zone and needs to be globally unique for the Azure region.
    # Each organization's domain name will have a CNAME record pointing to the domain name formed
    # by this value, which will be <dnslabel>.<region>.cloudapp.azure.com
    {{ optionalField "dnsLabel" .DnsLabel "" }}

    # Optional Helm chart overrides
    {{- renderSharedHelm .Helm | nindent 4 }}
  {{- end}}

  {{- with .Database }}

  database:
    serverName: {{ .ServerName }}
    {{ optionalField "postgresMajorVersion" .PostgresMajorVersion "" }}
    {{- if .Location }}
    location: {{ .Location }}
    {{- else }}
    # location: Defaults to defaultLocation
    {{- end }}
    {{- if .ComputeTier }}
    computeTier: {{ .ComputeTier }}
    {{- else }}
    # computeTier: Defaults to Burstable
    {{- end }}
    {{- if .VMSize }}
    vmSize: {{ .VMSize }}
    {{- else }}
    # vmSize: Defaults to Standard_B1ms
    {{- end }}
    {{- if .StorageSizeGB }}
    storageSizeGB: {{ .StorageSizeGB }}
    {{- else }}
    # storageSizeGB: Defaults to 32 (the minimum supported)
    {{- end }}
    {{- if .BackupRetentionDays }}
    backupRetentionDays: {{ .BackupRetentionDays }}
    {{- else }}
    # backupRetentionDays: Defaults to 7
    {{- end }}
    {{- if .BackupGeoRedundancy }}
    backupGeoRedundancy: {{ .BackupGeoRedundancy }}
    {{- else }}
    # backupGeoRedundancy: Defaults to false
    {{- end }}

    # Firewall rules to control where the database can be accessed from,
    # in addition to the control-plane cluster.
    {{- if .FirewallRules }}
    firewallRules:
      {{- range .FirewallRules }}
      - name: {{ .Name }}
        startIpAddress: {{ .StartIpAddress }}
        endIpAddress: {{ .EndIpAddress }}
      {{- end }}
    {{- else }}
    # firewallRules:
    #  - name:
    #    startIpAddress:
    #    endIpAddress:
    {{- end }}
  {{- else }}
  {{- end }}
  {{- if .NetworkSecurityPerimeter}}

  networkSecurityPerimeter:
    nspResourceGroup: {{ .NetworkSecurityPerimeter.NspResourceGroup }}
    nspName: {{ .NetworkSecurityPerimeter.NspName }}
    storageProfile:
      name: {{ .NetworkSecurityPerimeter.StorageProfile.Name }}
      mode: {{ .NetworkSecurityPerimeter.StorageProfile.Mode }}
  {{- end }}
{{- end }}

{{ if not .Organizations -}}
organizations: []
{{ else -}}
organizations:
  {{- range .Organizations }}
  - name: {{ .Name }}
    {{ if .SingleOrganizationCompatibilityMode -}}
    singleOrganizationCompatibilityMode: true
    {{ end -}}
    {{- with .Cloud -}}
    cloud:
      storage:
        # Storage accounts for buffers.
        buffers:
          {{- range .Storage.Buffers }}
          - name: {{ .Name }}
            {{ optionalField "location" .Location "defaults to defaultLocation" }}
            {{ optionalField "sku" .Sku "defaults to Standard_LRS" }}
            # dnsEndpointType can be set to `AzureDNSZone` when creating large number of accounts in a single subscription.
            {{ optionalField "dnsEndpointType" .DnsEndpointType "defaults to Standard." }}
          {{- end }}

        # The storage account where run logs will be stored.
        logs:
          name: {{ .Storage.Logs.Name }}
          {{ optionalField "location" .Storage.Logs.Location "defaults to defaultLocation" }}
          {{ optionalField "sku" .Storage.Logs.Sku "defaults to Standard_LRS" }}
          # dnsEndpointType can be set to `AzureDNSZone` when creating large number of accounts in a single subscription.
          {{ optionalField "dnsEndpointType" .Storage.Logs.DnsEndpointType "defaults to Standard." }}

      # An optional array of managed identities that will be created in the resource group.
      # These identities are available to runs as workload identities. When updating this list
      # both `tyger cloud install` and `tyger api installed` must be run.
      {{ if .Identities -}}
      identities:
        {{- range .Identities }}
        - {{ . }}
        {{- end }}
      {{- else -}}
      # identities:
      # - my-identity
      {{- end }}
    {{- end }}

    {{ with .Api -}}
    api:
      # The fully qualified domain name for the Tyger API.
      domainName: {{ .DomainName }}

      # Set to KeyVault if using a custom TLS certificate, otherwise set to LetsEncrypt
      tlsCertificateProvider: {{ .TlsCertificateProvider }}

      auth:
        tenantId: {{ .Auth.TenantID }}
        apiAppUri: {{ .Auth.ApiAppUri }}
        apiAppId: {{ .Auth.ApiAppId }}
        cliAppUri: {{ .Auth.CliAppUri }}
        cliAppId: {{ .Auth.CliAppId }}

      {{- if (and .Buffers (or .Buffers.ActiveLifetime .Buffers.SoftDeletedLifetime)) }}

      buffers:
        # TTL for active buffers before they are automatically soft-deleted (D.HH:MM:SS) (0 = never expire)
        {{ optionalField "activeLifetime" .Buffers.ActiveLifetime "defaults to 0.00:00" }}
        # TTL for soft-deleted buffers before they are automatically purged forever (D.HH:MM:SS) (0 = purge immediately)
        {{ optionalField "softDeletedLifetime" .Buffers.SoftDeletedLifetime "defaults to 1.00:00" }}
      {{- else }}

      # buffers:
        # TTL for active buffers before they are automatically soft-deleted (D.HH:MM:SS) (0 = never expire)
        # activeLifetime: defaults to 0.00:00
        # TTL for soft-deleted buffers before they are automatically purged forever (D.HH:MM:SS) (0 = purge immediately)
        # softDeletedLifetime: default to 1.00:00
      {{- end }}

      # Optional Helm chart overrides
      {{- renderOrgHelm .Helm | nindent 6 }}
    {{- end}}

  {{- end}}

{{- end }}
