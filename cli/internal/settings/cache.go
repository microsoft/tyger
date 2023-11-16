package settings

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"gopkg.in/yaml.v2"
)

const CacheFileEnvVarName = "TYGER_CACHE_FILE"

type Settings struct {
	ServerUri                      string `json:"serverUri"`
	ClientAppUri                   string `json:"clientAppUri,omitempty"`
	ClientId                       string `json:"clientId,omitempty"`
	LastToken                      string `json:"lastToken,omitempty"`
	LastTokenExpiry                int64  `json:"lastTokenExpiration,omitempty"`
	Principal                      string `json:"principal,omitempty"`
	CertPath                       string `json:"certPath,omitempty"`
	CertThumbprint                 string `json:"certThumbprint,omitempty"`
	Authority                      string `json:"authority,omitempty"`
	Audience                       string `json:"audience,omitempty"`
	FullTokenCache                 string `json:"fullTokenCache,omitempty"`
	DataPlaneProxy                 string `json:"dataPlaneProxy,omitempty"`
	IgnoreSystemProxySettings      bool   `json:"ignoreSystemProxySettings,omitempty"`
	SkipTlsCertificateVerification bool   `json:"insecureSkipTlsCertificateVerification,omitempty"`
}

func GetCachePath() (string, error) {
	var cacheDir string
	var fileName string

	if path := os.Getenv(CacheFileEnvVarName); path != "" {
		cacheDir = filepath.Dir(path)
		fileName = filepath.Base(path)
	} else {
		var err error
		cacheDir, err = os.UserCacheDir()
		if err != nil {
			return "", fmt.Errorf("unable to locate cache directory: %w; to provide a file path directly, set the $%s environment variable", err, CacheFileEnvVarName)
		}
		cacheDir = filepath.Join(cacheDir, "tyger")
		fileName = ".tyger"
	}

	err := os.MkdirAll(cacheDir, 0775)
	if err != nil {
		return "", fmt.Errorf("unable to create %s directory", cacheDir)
	}
	return filepath.Join(cacheDir, fileName), nil
}

func (s *Settings) Persist() error {
	path, err := GetCachePath()
	if err == nil {
		var bytes []byte
		bytes, err = yaml.Marshal(s)
		if err == nil {
			err = persistCacheContents(path, bytes)
		}
	}

	if err != nil {
		return fmt.Errorf("failed to write auth cache: %w", err)
	}

	return nil
}

func persistCacheContents(path string, bytes []byte) error {
	// Write to a temp file in the same directory first
	tempFileName := fmt.Sprintf("%s.`%v`", path, uuid.New())
	defer os.Remove(tempFileName)
	if err := os.WriteFile(tempFileName, bytes, 0600); err != nil {
		return err
	}

	// Now rename the temp file to the final name.
	// If the file is not writable due to a permission error,
	// it could be because another process is holding the file open.
	// In that case, we retry over a short period of time.
	var err error
	for i := 0; i < 50; i++ {
		if i > 0 {
			time.Sleep(100 * time.Millisecond)
		}

		err = os.Rename(tempFileName, path)
		if err == nil || !errors.Is(err, os.ErrPermission) {
			break
		}
	}

	return err
}

func GetPersistedSettings() (*Settings, error) {
	si := &Settings{}
	path, err := GetCachePath()
	if err != nil {
		return si, err
	}

	bytes, err := readCachedContents(path)

	if err != nil {
		return si, err
	}

	err = yaml.Unmarshal(bytes, &si)
	return si, err
}

func readCachedContents(path string) ([]byte, error) {
	var bytes []byte
	var err error

	// If the file is not readable due to a permission error,
	// it could be because another process is holding the file open.
	// In that case, we retry over a short period of time.
	for i := 0; i < 50; i++ {
		if i > 0 {
			time.Sleep(100 * time.Millisecond)
		}

		bytes, err = os.ReadFile(path)
		if err == nil || !errors.Is(err, os.ErrPermission) {
			break
		}
	}

	return bytes, err
}
