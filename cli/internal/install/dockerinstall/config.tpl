kind: docker

# Optionally specify the user id that the services will run as
userId:

# Optionally specify the user group ID that will be allowed
# to access the Tyger API
allowedGroupId:

# The port on which the data plane API will listen
dataPlanePort: {{ .DataPlanePort }}

# Specify asymmetric signing keys for the data plane service.
# These can be generated with `tyger api generate-signing-key`
# These files must not be stored in a source code repository.
signingKeys:
  primary:
    public: {{ .PublicSigningKeyPath }}
    private: {{ .PrivateSigningKeyPath }}

  # Optionally specify a secondary key pair.
  # The primary key will always be used for signing requests.
  # Signature validation will accept payloads signed with either the
  # primary or secondary key.
  # secondary:
  #  private:
  #  public:

# Optionally specify settings for the Docker network to be created
# network:
#  subnet: 172.20.0.0/16

# Optionally specify container images to use.
# controlPlaneImage:
# dataPlaneImage:
# bufferSidecarImage:
# gatewayImage:

# Settings for all buffers
buffers:
  # TTL for active buffers before they are automatically soft-deleted (D.HH:MM:SS) (0 = never expire)
  activeLifetime: 0.00:00

  # TTL for soft-deleted buffers before they are automatically purged forever (D.HH:MM:SS) (0 = purge immediately)
  softDeletedLifetime: 1.00:00
