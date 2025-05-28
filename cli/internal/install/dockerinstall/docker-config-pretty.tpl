kind: docker
{{- if .EnvironmentName }}

# Optionally give this environment a name.
environmentName: {{ .EnvironmentName }}
{{- end }}
{{- if .InstallationPath }}

# The path where Tyger will be installed
installationPath: {{ .InstallationPath }}
{{- end }}

# Optionally specify the user id that the services will run as
{{ if .UserId -}}
userId: {{ .UserId }}
{{- else -}}
# userId:
{{- end }}

# Optionally specify the user group ID that will be allowed
# to access the Tyger API
{{ if .AllowedGroupId -}}
allowedGroupId: {{ .AllowedGroupId }}
{{- else -}}
# allowedGroupId:
{{- end }}

# The port on which the data plane API will listen
dataPlanePort: {{ .DataPlanePort }}
{{- if .UseGateway }}

# Whether to use the gateway service
useGateway: {{ .UseGateway }}
{{- end }}

# Specify asymmetric signing keys for the data plane service.
# These can be generated with `tyger api generate-signing-key`
# These files must not be stored in a source code repository.
signingKeys:
  {{ with .SigningKeys.Primary -}}
  primary:
    public: {{ .PublicKey }}
    private: {{ .PrivateKey }}
  {{- else -}}
  # primary:
  #   public:
  #   private:
  {{- end }}

  # Optionally specify a secondary key pair.
  # The primary key will always be used for signing requests.
  # Signature validation will accept payloads signed with either the
  # primary or secondary key.
  {{ with .SigningKeys.Secondary -}}
  secondary:
    public: {{ .PublicKey }}
    private: {{ .PrivateKey }}
  {{- else -}}
  # secondary:
  #   public:
  #   private:
  {{- end }}

# Optionally specify settings for the Docker network to be created
{{ with .Network -}}
network:
  subnet: {{ .Subnet }}
{{- else -}}
# network:
#   subnet: 172.20.0.0/16
{{- end }}

# Optionally specify container images to use.
{{ optionalField "controlPlaneImage" .ControlPlaneImage "" }}
{{ optionalField "dataPlaneImage" .DataPlaneImage "" }}
{{ optionalField "bufferSidecarImage" .BufferSidecarImage "" }}
{{ optionalField "gatewayImage" .GatewayImage "" }}
{{ optionalField "postgresImage" .PostgresImage "" }}
{{ optionalField "marinerImage" .MarinerImage "" -}}
{{ if .InitialDatabaseVersion -}}
# Undocumented field for development
initialDatabaseVersion: {{ .InitialDatabaseVersion }}
{{- end }}
{{- if (and .Buffers (or .Buffers.ActiveLifetime .Buffers.SoftDeletedLifetime)) }}

buffers:
  # TTL for active buffers before they are automatically soft-deleted (D.HH:MM:SS) (0 = never expire)
  {{ optionalField "activeLifetime" .Buffers.ActiveLifetime "defaults to 0.00:00" }}

  # TTL for soft-deleted buffers before they are automatically purged forever (D.HH:MM:SS) (0 = purge immediately)
  {{ optionalField "softDeletedLifetime" .Buffers.SoftDeletedLifetime "defaults to 1.00:00" }}
{{- else }}

# buffers:
#   TTL for active buffers before they are automatically soft-deleted (D.HH:MM:SS) (0 = never expire)
#   activeLifetime: defaults to 0.00:00
#
#   TTL for soft-deleted buffers before they are automatically purged forever (D.HH:MM:SS) (0 = purge immediately)
#   softDeletedLifetime: default to 1.00:00
{{- end }}
