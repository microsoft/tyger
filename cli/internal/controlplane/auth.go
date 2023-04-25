package controlplane

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/controlplane/model"
	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/cache"
	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/confidential"
	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/public"
	"github.com/google/uuid"
	"sigs.k8s.io/yaml"
)

const (
	CacheFileEnvVarName                 = "TYGER_CACHE_FILE"
	cliClientId                         = "d81fc78a-b30f-49ee-8697-54775895e218" // this needs to be the app ID and not the identifier URI, otherwise the refresh tokens will not work.
	userScope                           = "Read.Write"
	servicePrincipalScope               = ".default"
	discardTokenIfExpiringWithinSeconds = 10 * 60
)

type ServiceInfo interface {
	GetServerUri() string
	GetPrincipal() string
	GetAccessToken() (string, error)
	GetDataPlaneProxy() string
}

type AuthConfig struct {
	ServerUri        string `json:"serverUri"`
	ServicePrincipal string `json:"servicePrincipal,omitempty"`
	CertificatePath  string `json:"certificatePath,omitempty"`
	UseDeviceCode    bool
	Persisted        bool
}

type serviceInfo struct {
	ServerUri          string `json:"serverUri"`
	LastToken          string `json:"lastToken,omitempty"`
	LastTokenExpiry    int64  `json:"lastTokenExpiration,omitempty"`
	Principal          string `json:"principal,omitempty"`
	CertPath           string `json:"certPath,omitempty"`
	Authority          string `json:"authority,omitempty"`
	Audience           string `json:"audience,omitempty"`
	FullCache          string `json:"fullCache,omitempty"`
	DataPlaneProxy     string `json:"dataPlaneProxy,omitempty"`
	confidentialClient *confidential.Client
}

func (c *serviceInfo) GetServerUri() string {
	return c.ServerUri
}

func (c *serviceInfo) GetPrincipal() string {
	return c.Principal
}

func (c *serviceInfo) GetDataPlaneProxy() string {
	return c.DataPlaneProxy
}

func Login(options AuthConfig) (ServiceInfo, error) {
	normalizedServerUri, err := normalizeServerUri(options.ServerUri)
	if err != nil {
		return nil, err
	}
	options.ServerUri = normalizedServerUri
	serviceMetadata, err := getServiceMetadata(options.ServerUri)
	if err != nil {
		return nil, err
	}

	si := &serviceInfo{
		ServerUri:      options.ServerUri,
		Authority:      serviceMetadata.Authority,
		Audience:       serviceMetadata.Audience,
		Principal:      options.ServicePrincipal,
		CertPath:       options.CertificatePath,
		DataPlaneProxy: serviceMetadata.DataPlaneProxy,
	}

	if serviceMetadata.Authority != "" {
		useServicePrincipal := options.ServicePrincipal != ""

		var authResult public.AuthResult
		if useServicePrincipal {
			authResult, err = si.performServicePrincipalLogin()
		} else {
			authResult, err = si.performUserLogin(options.UseDeviceCode)
		}
		if err != nil {
			return nil, err
		}

		si.LastToken = authResult.AccessToken
		si.LastTokenExpiry = authResult.ExpiresOn.Unix()

		if !useServicePrincipal {
			si.Principal = authResult.IDToken.PreferredUsername
		}
	}

	if options.Persisted {
		err = si.persist()
	}

	return si, err
}

func normalizeServerUri(uri string) (string, error) {
	uri = strings.TrimRight(uri, "/")
	parsedUrl, err := url.Parse(uri)
	if err != nil || !parsedUrl.IsAbs() {
		return uri, errors.New("a valid absolute uri is required")
	}

	return uri, err
}

func Logout() error {
	return (&serviceInfo{}).persist()
}

func (c *serviceInfo) GetAccessToken() (string, error) {
	// Quick check to see if the last token is still valid.
	// This token is in the full MSAL token cache, but unfortunately calling
	// client.AcquireTokenSilent currenly always calls an AAD discovery endpoint, which
	// can take > 0.5 seconds. So we store it ourselves and use it here if it is still valid.
	if c.LastTokenExpiry-discardTokenIfExpiringWithinSeconds > time.Now().UTC().Unix() && c.LastToken != "" {
		return c.LastToken, nil
	}

	if c.Authority == "" {
		return "", nil
	}

	var authResult public.AuthResult
	if c.CertPath != "" {
		var err error
		authResult, err = c.performServicePrincipalLogin()
		if err != nil {
			return "", err
		}
	} else {
		// fall back to using the refresh token from the cache
		client, err := public.New(
			cliClientId,
			public.WithAuthority(c.Authority),
			public.WithCache(c),
		)

		if err != nil {
			return "", err
		}

		accounts, err := client.Accounts(context.Background())
		if err != nil {
			return "", fmt.Errorf("unable to get accounts from token cache: %v", err)
		}
		if len(accounts) != 1 {
			return "", errors.New("corrupted token cache")
		}

		authResult, err = client.AcquireTokenSilent(context.Background(), []string{fmt.Sprintf("%s/%s", c.Audience, userScope)}, public.WithSilentAccount(accounts[0]))
		if err != nil {
			return "", err
		}
	}

	c.LastToken = authResult.AccessToken
	c.LastTokenExpiry = authResult.ExpiresOn.Unix()

	return authResult.AccessToken, c.persist()
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
			return "", fmt.Errorf("unable to locate cache directory: %v; to provide a file path directly, set the $%s environment variable", err, CacheFileEnvVarName)
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

func (si *serviceInfo) persist() error {
	path, err := GetCachePath()
	if err == nil {
		var bytes []byte
		bytes, err = yaml.Marshal(si)
		if err == nil {
			err = persistCacheContents(path, bytes)
		}
	}

	if err != nil {
		return fmt.Errorf("failed to write auth cache: %v", err)
	}

	return nil
}

func persistCacheContents(path string, bytes []byte) error {
	// Write to a temp file in the same directory first
	tempFileName := fmt.Sprintf("%s.%v", path, uuid.New())
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

func GetPersistedServiceInfo() (*serviceInfo, error) {
	si := &serviceInfo{}
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

func getServiceMetadata(serverUri string) (*model.ServiceMetadata, error) {
	resp, err := NewRetryableClient().Get(fmt.Sprintf("%s/v1/metadata", serverUri))
	if err != nil {
		return nil, err
	}
	serviceMetadata := &model.ServiceMetadata{}
	if err := json.NewDecoder(resp.Body).Decode(serviceMetadata); err != nil {
		return nil, errors.New("the server URL does not appear to point to a valid tyger server")
	}

	return serviceMetadata, nil
}

func (si *serviceInfo) performServicePrincipalLogin() (authResult confidential.AuthResult, err error) {
	newClient := si.confidentialClient == nil
	if newClient {
		certBytes, err := os.ReadFile(si.CertPath)
		if err != nil {
			return authResult, fmt.Errorf("unable to read certificate file: %v", err)
		}

		certs, privateKey, err := confidential.CertFromPEM(certBytes, "")
		if err != nil {
			return authResult, fmt.Errorf("error decoding certificate: %v", err)
		}

		cred, err := confidential.NewCredFromCert(certs, privateKey)
		if err != nil {
			return authResult, fmt.Errorf("error creating credential: %v", err)
		}

		client, err := confidential.New(si.Authority, si.Principal, cred)
		if err != nil {
			return authResult, err
		}
		si.confidentialClient = &client
	}

	scopes := []string{fmt.Sprintf("%s/%s", si.Audience, servicePrincipalScope)}
	if !newClient {
		authResult, err = si.confidentialClient.AcquireTokenSilent(context.Background(), scopes)
	}
	if newClient || err != nil {
		authResult, err = si.confidentialClient.AcquireTokenByCredential(context.Background(), scopes)
	}

	return authResult, err
}

func (si *serviceInfo) performUserLogin(useDeviceCode bool) (authResult public.AuthResult, err error) {
	client, err := public.New(
		cliClientId,
		public.WithAuthority(si.Authority),
		public.WithCache(si),
	)
	if err != nil {
		return
	}

	scopes := []string{fmt.Sprintf("%s/%s", si.Audience, userScope)}

	if useDeviceCode {
		dc, err := client.AcquireTokenByDeviceCode(context.Background(), scopes)
		if err != nil {
			return authResult, err
		}

		fmt.Println(dc.Result.Message)
		return dc.AuthenticationResult(context.Background())
	}

	// 🚨HACK ALERT🚨
	// If the BROWSER variable is set, its value should be the preferred
	// executable to call to bring up a browser window.
	// The underlying library that gets called here (github.com/pkg/browser) only looks for the executables
	// "xdg-open", "x-www-browser" and "www-browser", so we put put in a little hack
	// to prepend a temp dir to the PATH variable and add an executable shim script called
	// "xdg-open" that calls the script in the the BROWSER variable.
	// TODO: this has only been tested on Linux.
	// TODO: The value could actually be a separated list of executables like in
	// https://docs.python.org/3/library/webbrowser.html
	if browserVar := os.Getenv("BROWSER"); browserVar != "" {
		if _, err := os.Stat(browserVar); err == nil {
			tempDir, createDirErr := os.MkdirTemp("", "prefix")
			if createDirErr == nil {
				defer os.RemoveAll(tempDir)
				shimPath := filepath.Join(tempDir, "xdg-open")
				shimContents := fmt.Sprintf(`#!/usr/bin/env sh
					set -eu
					%s "$@"`, browserVar)

				if err = os.WriteFile(shimPath, []byte(shimContents), 0700); err == nil {
					os.Setenv("PATH", fmt.Sprintf("%s%s%s", tempDir, string(filepath.ListSeparator), os.Getenv("PATH")))
				}
			}
		}
	}

	timer := time.AfterFunc(time.Second, func() {
		fmt.Println("The default web browser has been opened. Please continue the login in the web browser. " +
			"If no web browser is available or if the web browser fails to open, use the device code flow with `tyger login --use-device-code`.")
	})

	authResult, err = client.AcquireTokenInteractive(context.Background(), scopes, public.WithRedirectURI("http://localhost:41087"))
	timer.Stop()
	if err == nil {
		return authResult, err
	}

	var exitError *exec.ExitError
	if errors.Is(err, exec.ErrNotFound) || errors.As(err, &exitError) {
		// this means that we were not able to bring up the brower. Fall back to using the device code flow.
		return si.performUserLogin(true)
	}

	return authResult, err
}

// Implementing the cache.ExportReplace interface to read in the token cache
func (si *serviceInfo) Replace(ctx context.Context, unmarshaler cache.Unmarshaler, hints cache.ReplaceHints) error {
	data, err := base64.StdEncoding.DecodeString(si.FullCache)
	if err == nil {
		unmarshaler.Unmarshal(data)
	}

	return err
}

// Implementing the cache.ExportReplace interface to write out the token cache
func (si *serviceInfo) Export(ctx context.Context, marshaler cache.Marshaler, hints cache.ExportHints) error {
	data, err := marshaler.Marshal()
	if err == nil {
		si.FullCache = base64.StdEncoding.EncodeToString(data)
	}

	return err
}
