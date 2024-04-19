kind: docker

# Specify asymmetric signing keys for the data plane service.
# These can be generated with `tyger api generate-signing-key`
signingKeys:
  primary:
    public: {{ .PublicSigningKeyPath }}
    private: {{ .PrivateSigningKeyPath }}

  # Optionally specify a secondary key pair.
  # The primary key will always be used for sigining requests.
  # Signature validation will accept payloads signed with either the
  # primary or secondary key.

  # secondary:
  #  private:
  #  public:

# useGateway:

# Optionally specify container images to use
# controlPlaneImage:
# dataPlaneImage:
# bufferSidecarImage:
# gatewayImage:
# gatewayImage:
