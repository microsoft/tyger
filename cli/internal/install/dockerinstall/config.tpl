kind: docker

# Optionally specify the user id that the services will run as
userId:

# Optionally specify the user group ID that will be allowed
# to access the Tyger API
allowedGroupId:

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

# Optionally specify container images to use.
# controlPlaneImage:
# dataPlaneImage:
# bufferSidecarImage:
# gatewayImage:
# gatewayImage:
