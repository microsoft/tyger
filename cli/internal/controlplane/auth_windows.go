package controlplane

import (
	"context"
	"crypto"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"syscall"
	"time"
	"unsafe"

	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/confidential"
	"github.com/golang-jwt/jwt/v4"
	"github.com/google/uuid"
	"golang.org/x/sys/windows"
)

const (
	bCryptPadPkcs1 uintptr = 0x2 // BCRYPT_PAD_PKCS1
)

var (
	nCrypt = windows.MustLoadDLL("ncrypt.dll")

	nCryptSignHash = nCrypt.MustFindProc("NCryptSignHash")

	bCryptSha256Algorithm = toUtf16String("SHA256") // BCRYPT_SHA256_ALGORITHM

	mySystemStoreName = toUtf16String("MY")
)

// paddingInfo is the BCRYPT_PKCS1_PADDING_INFO struct in bcrypt.h.
type paddingInfo struct {
	pszAlgID *uint16
}

type privateKeyHandle windows.Handle

func createCredentialFromSystemCertificateStore(thumbprint string) (confidential.Credential, error) {
	// look in the the current user's "MY" store first, then the local machine's "MY" store
	certContext, err := findCertificateByThumbprint(thumbprint, windows.CERT_SYSTEM_STORE_CURRENT_USER)
	if err != nil {
		var errno syscall.Errno
		if errors.As(err, &errno) && uintptr(errno) == uintptr(windows.CRYPT_E_NOT_FOUND) {
			var systemStoreErr error
			certContext, systemStoreErr = findCertificateByThumbprint(thumbprint, windows.CERT_SYSTEM_STORE_LOCAL_MACHINE)
			if systemStoreErr != nil {
				return confidential.Credential{}, fmt.Errorf("failed to find certificate: %w", systemStoreErr)
			}
		} else {
			return confidential.Credential{}, fmt.Errorf("failed to find certificate: %w", err)
		}
	}

	// We cannot do `defer windows.CertFreeCertificateContext(certContext)` here because
	// the certificate's private key is used in the callback function, after this function
	// has returned. So this leaks, but there is only ever one of these per process, so
	// it should not be a problem.

	privateKey, err := acquirePrivateKey(certContext)
	if err != nil {
		return confidential.Credential{}, fmt.Errorf("failed to acquire private key: %w", err)
	}

	x5tHeaderValue := base64.URLEncoding.EncodeToString(getThumbprintBytes(certContext))

	return confidential.NewCredFromAssertionCallback(func(ctx context.Context, aro confidential.AssertionRequestOptions) (string, error) {
		signingMethod := &rsaCngSiningMethod{}
		token := jwt.NewWithClaims(signingMethod, jwt.MapClaims{
			"aud": aro.TokenEndpoint,
			"exp": json.Number(strconv.FormatInt(time.Now().Add(10*time.Minute).Unix(), 10)),
			"iss": aro.ClientID,
			"jti": uuid.New().String(),
			"nbf": json.Number(strconv.FormatInt(time.Now().Unix(), 10)),
			"sub": aro.ClientID,
		})
		token.Header = map[string]interface{}{
			"alg": "RS256",
			"typ": "JWT",
			"x5t": x5tHeaderValue,
		}

		assertion, err := token.SignedString(privateKey)
		if err != nil {
			return "", fmt.Errorf("unable to sign a JWT token using private key: %w", err)
		}
		return assertion, nil
	}), nil
}

func findCertificateByThumbprint(thumbprint string, storeLocation uint32) (*windows.CertContext, error) {
	store, err := windows.CertOpenStore(
		windows.CERT_STORE_PROV_SYSTEM,
		0,
		0,
		storeLocation|windows.CERT_STORE_READONLY_FLAG,
		uintptr(unsafe.Pointer(mySystemStoreName)))
	if store == 0 {
		return nil, fmt.Errorf("failed to open store: %w", err)
	}
	defer windows.CertCloseStore(store, 0)

	var hash windows.CryptHashBlob
	hashBytes, err := hex.DecodeString(thumbprint)
	if err != nil {
		return nil, err
	}

	hash.Size = uint32(len(hashBytes))
	hash.Data = &hashBytes[0]
	pointer := unsafe.Pointer(&hash)

	var certContext *windows.CertContext
	certContext, err = windows.CertFindCertificateInStore(
		store,
		0,
		0,
		windows.CERT_FIND_SHA1_HASH,
		pointer,
		certContext)

	if err != nil {
		return nil, fmt.Errorf("certificate not found: %w", err)
	}

	return certContext, nil
}

func acquirePrivateKey(cert *windows.CertContext) (privateKeyHandle, error) {
	var (
		keyHandle windows.Handle
		keySpec   uint32
		mustFree  bool
	)

	err := windows.CryptAcquireCertificatePrivateKey(
		cert,
		windows.CRYPT_ACQUIRE_ONLY_NCRYPT_KEY_FLAG|windows.CRYPT_ACQUIRE_CACHE_FLAG,
		nil,
		&keyHandle,
		&keySpec,
		&mustFree,
	)

	if err != nil {
		return 0, fmt.Errorf("failed to acquire private key: %w", err)
	}

	if mustFree {
		panic("mustFree should not be true after CryptAcquireCertificatePrivateKey")
	}

	return privateKeyHandle(keyHandle), nil
}

func signPayload(payload []byte, privateKey privateKeyHandle, hashFunction crypto.Hash) ([]byte, error) {
	padInfo := paddingInfo{}
	switch hashFunction {
	case crypto.SHA256:
		padInfo.pszAlgID = bCryptSha256Algorithm
	default:
		return nil, fmt.Errorf("unsupported hash algorithm: %s", hashFunction.String())
	}

	hasher := hashFunction.New()
	hasher.Write(payload)
	digest := hasher.Sum(nil)

	var size uint32
	// Obtain the size of the signature
	r, _, err := nCryptSignHash.Call(
		uintptr(privateKey),
		uintptr(unsafe.Pointer(&padInfo)),
		uintptr(unsafe.Pointer(&digest[0])),
		uintptr(len(digest)),
		0,
		0,
		uintptr(unsafe.Pointer(&size)),
		bCryptPadPkcs1)
	if r != 0 {
		return nil, fmt.Errorf("NCryptSignHash returned %X during size check: %w", r, err)
	}

	// Obtain the signature data
	sig := make([]byte, size)
	r, _, err = nCryptSignHash.Call(
		uintptr(privateKey),
		uintptr(unsafe.Pointer(&padInfo)),
		uintptr(unsafe.Pointer(&digest[0])),
		uintptr(len(digest)),
		uintptr(unsafe.Pointer(&sig[0])),
		uintptr(size),
		uintptr(unsafe.Pointer(&size)),
		bCryptPadPkcs1)
	if r != 0 {
		return nil, fmt.Errorf("NCryptSignHash returned %X during signing: %w", r, err)
	}

	return sig[:size], nil
}

func getEncodedCert(cert *windows.CertContext) []byte {
	return unsafe.Slice(cert.EncodedCert, cert.Length)
}

// Runs the asn1.Der bytes through sha1 for use in the x5t parameter of JWT.
// https://tools.ietf.org/html/rfc7517#section-4.8
func getThumbprintBytes(cert *windows.CertContext) []byte {
	hash := sha1.Sum(getEncodedCert(cert))
	return hash[:]
}

func toUtf16String(s string) *uint16 {
	res, err := windows.UTF16PtrFromString(s)
	if err != nil {
		panic(err)
	}
	return res
}

// implements jwt.SigningMethod
type rsaCngSiningMethod struct{}

func (m *rsaCngSiningMethod) Alg() string {
	return jwt.SigningMethodRS256.Alg()
}

func (m *rsaCngSiningMethod) Sign(signingString string, key any) (string, error) {
	privateKey := key.(privateKeyHandle)
	sig, err := signPayload([]byte(signingString), privateKey, crypto.SHA256)
	if err != nil {
		return "", fmt.Errorf("failed to sign payload: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(sig), nil
}

func (m *rsaCngSiningMethod) Verify(signingString, signature string, key any) error {
	return errors.New("not implemented")
}
