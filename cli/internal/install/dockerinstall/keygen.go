package dockerinstall

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
)

func GenerateSigningKeyPair(publicPath, privatePath string) error {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("error generating private key: %w", err)
	}

	publicKeyEncoded, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		return fmt.Errorf("error encoding public key: %w", err)
	}

	publicFile, err := os.Create(publicPath)
	if err != nil {
		return fmt.Errorf("error creating public key file: %w", err)
	}
	defer publicFile.Close()

	if err := pem.Encode(publicFile, &pem.Block{Type: "PUBLIC KEY", Bytes: publicKeyEncoded}); err != nil {
		return fmt.Errorf("error encoding public key to PEM file: %w", err)
	}

	privateKeyEncoded, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return fmt.Errorf("error encoding private key: %w", err)
	}

	privateFile, err := os.Create(privatePath)
	if err != nil {
		return fmt.Errorf("error creating private key file: %w", err)
	}
	defer privateFile.Close()

	if err := pem.Encode(privateFile, &pem.Block{Type: "PRIVATE KEY", Bytes: privateKeyEncoded}); err != nil {
		return fmt.Errorf("error encoding private key to PEM file: %w", err)
	}

	return nil
}
