// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

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

func CreateSignatureValidationFunc(primarySigningPublicKeyPath, secondarySigningPublicKeyPath string) (ValidateSignatureFunc, error) {
	type ValidateSignatureFuncFromHash func(sha256Hash [32]byte, signature []byte) bool
	createSingle := func(certPath string) (ValidateSignatureFuncFromHash, error) {
		pemBytes, err := os.ReadFile(certPath)
		if err != nil {
			return nil, fmt.Errorf("failed to public key pem file at %s: %w", certPath, err)
		}

		blockPub, _ := pem.Decode(pemBytes)

		pub, err := x509.ParsePKIXPublicKey(blockPub.Bytes)
		if err != nil {
			return nil, fmt.Errorf("failed to decode public key from pem file at %s: %w", certPath, err)
		}

		switch key := pub.(type) {
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

	if primarySigningPublicKeyPath == "" {
		return nil, fmt.Errorf("a primary public key file is required")
	}

	primary, err := createSingle(primarySigningPublicKeyPath)
	if err != nil {
		return nil, err
	}

	var secondary ValidateSignatureFuncFromHash

	if secondarySigningPublicKeyPath != "" {
		var err error
		secondary, err = createSingle(secondarySigningPublicKeyPath)
		if err != nil {
			return nil, err
		}
	}

	return func(data []byte, signature []byte) bool {
		sha256Hash := sha256.Sum256(data)
		return primary(sha256Hash, signature) || (secondary != nil && secondary(sha256Hash, signature))
	}, nil
}
