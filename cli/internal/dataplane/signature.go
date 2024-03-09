package dataplane

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
)

type ValidateSignatureFunc func(data []byte, signature []byte) bool

func CreateSignatureValidationFunc(primaryPublicPem, secondaryPublicPem string) (ValidateSignatureFunc, error) {
	type ValidateSignatureFuncFromHash func(sha256Hash [32]byte, signature []byte) bool
	createSingle := func(certPath string) (ValidateSignatureFuncFromHash, error) {
		certBytes, err := os.ReadFile(certPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read certificate at %s: %w", certPath, err)
		}

		block, _ := pem.Decode(certBytes)
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("failed to parse  certificate at %s: %w", certPath, err)
		}

		switch key := cert.PublicKey.(type) {
		case *rsa.PublicKey:
			return func(sha256Hash [32]byte, signature []byte) bool {
				return rsa.VerifyPKCS1v15(key, crypto.SHA256, sha256Hash[:], signature) == nil
			}, nil
		case *ecdsa.PublicKey:
			return func(sha256Hash [32]byte, signature []byte) bool {
				return ecdsa.VerifyASN1(key, sha256Hash[:], signature)
			}, nil
		default:
			return nil, fmt.Errorf("unsupported public key type %T at path %s", key, certPath)
		}
	}

	if primaryPublicPem == "" {
		return nil, fmt.Errorf("a primary public key file is required")
	}

	primary, err := createSingle(primaryPublicPem)
	if err != nil {
		return nil, err
	}

	var secondary ValidateSignatureFuncFromHash

	if secondaryPublicPem != "" {
		var err error
		secondary, err = createSingle(secondaryPublicPem)
		if err != nil {
			return nil, err
		}
	}

	return func(data []byte, signature []byte) bool {
		sha256Hash := sha256.Sum256(data)
		return primary(sha256Hash, signature) || (secondary != nil && secondary(sha256Hash, signature))
	}, nil
}
