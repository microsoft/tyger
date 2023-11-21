package controlplane

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/cache"
	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/confidential"
	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/public"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/hashicorp/go-retryablehttp"
	"github.com/mattn/go-ieproxy"
	"github.com/microsoft/tyger/cli/internal/controlplane/model"
	"github.com/microsoft/tyger/cli/internal/httpclient"
	"github.com/microsoft/tyger/cli/internal/settings"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"sigs.k8s.io/yaml"
)

const (
	CacheFileEnvVarName                 = "TYGER_CACHE_FILE"
	userScope                           = "Read.Write"
	servicePrincipalScope               = ".default"
	discardTokenIfExpiringWithinSeconds = 10 * 60
)

type LoginConfig struct {
	ServerUri                       string `json:"serverUri"`
	ServicePrincipal                string `json:"servicePrincipal,omitempty"`
	CertificatePath                 string `json:"certificatePath,omitempty"`
	CertificateThumbprint           string `json:"certificateThumbprint,omitempty"`
	Proxy                           string `json:"proxy,omitempty"`
	DisableTlsCertificateValidation bool   `json:"disableTlsCertificateValidation,omitempty"`
	LogPath                         string `json:"logPath,omitempty"`
	UseDeviceCode                   bool   `json:"-"`
	Persisted                       bool   `json:"-"`
}

type serviceInfo struct {
	ServerUri                       string `json:"serverUri"`
	parsedServerUri                 *url.URL
	ClientAppUri                    string `json:"clientAppUri,omitempty"`
	ClientId                        string `json:"clientId,omitempty"`
	LastToken                       string `json:"lastToken,omitempty"`
	LastTokenExpiry                 int64  `json:"lastTokenExpiration,omitempty"`
	Principal                       string `json:"principal,omitempty"`
	CertPath                        string `json:"certPath,omitempty"`
	CertThumbprint                  string `json:"certThumbprint,omitempty"`
	Authority                       string `json:"authority,omitempty"`
	Audience                        string `json:"audience,omitempty"`
	FullCache                       string `json:"fullCache,omitempty"`
	DataPlaneProxy                  string `json:"dataPlaneProxy,omitempty"`
	parsedDataPlaneProxy            *url.URL
	Proxy                           string `json:"proxy,omitempty"`
	parsedProxy                     *url.URL
	DisableTlsCertificateValidation bool `json:"disableTlsCertificateValidation,omitempty"`
	confidentialClient              *confidential.Client
}

func (c *serviceInfo) GetServerUri() *url.URL {
	return c.parsedServerUri
}

func (c *serviceInfo) GetPrincipal() string {
	return c.Principal
}

func (c *serviceInfo) GetProxyFunc() func(*http.Request) (*url.URL, error) {
	var base func(*http.Request) (*url.URL, error)
	switch c.Proxy {
	case "none":
		base = func(r *http.Request) (*url.URL, error) {
			return nil, nil
		}
	case "", "auto", "automatic":
		base = ieproxy.GetProxyFunc()
	default:
		base = func(r *http.Request) (*url.URL, error) {
			return c.parsedProxy, nil
		}
	}

	var withDataPlaneCheck func(*http.Request) (*url.URL, error)
	if c.parsedDataPlaneProxy == nil {
		withDataPlaneCheck = base
	} else {
		controlPlaneUrl := c.parsedServerUri
		withDataPlaneCheck = func(r *http.Request) (*url.URL, error) {
			if r.URL.Scheme == controlPlaneUrl.Scheme &&
				r.URL.Host == controlPlaneUrl.Host &&
				strings.HasPrefix(r.URL.Path, controlPlaneUrl.Path) {
				// This is a request to the control plane
				return base(r)
			}
			return c.parsedDataPlaneProxy, nil
		}
	}

	withHttpCheck := func(r *http.Request) (*url.URL, error) {
		if r.URL.Scheme == "http" {
			// We will not use an HTTP proxy when when not using TLS.
			// The only supported scenario for using http and not https is
			// when using using tyger to call tyger-proxy. In that case, we
			// want to connect to tyger-proxy directly, and not through a proxy.
			return nil, nil
		}
		return withDataPlaneCheck(r)
	}

	if log.Logger.GetLevel() <= zerolog.TraceLevel {
		return func(r *http.Request) (*url.URL, error) {
			proxy, err := withHttpCheck(r)
			if err == nil {
				var proxyString string
				if proxy != nil {
					proxyString = proxy.String()
				} else {
					proxyString = ""
				}
				log.Ctx(r.Context()).Trace().Msgf("Issuing request to host '%s' via proxy '%s'", r.URL.Host, proxyString)
			}
			return proxy, err
		}
	}
	return withHttpCheck
}

func (c *serviceInfo) GetDisableTlsCertificateValidation() bool {
	return c.DisableTlsCertificateValidation
}

func Login(ctx context.Context, options LoginConfig) (context.Context, settings.ServiceInfo, error) {
	normalizedServerUri, err := normalizeServerUri(options.ServerUri)
	if err != nil {
		return nil, nil, err
	}
	options.ServerUri = normalizedServerUri.String()

	si := &serviceInfo{
		ServerUri:                       options.ServerUri,
		parsedServerUri:                 normalizedServerUri,
		Principal:                       options.ServicePrincipal,
		CertPath:                        options.CertificatePath,
		CertThumbprint:                  options.CertificateThumbprint,
		Proxy:                           options.Proxy,
		DisableTlsCertificateValidation: options.DisableTlsCertificateValidation,
	}

	if err := validateServiceInfo(si); err != nil {
		return ctx, nil, err
	}

	// store in context so that the HTTP client can pick up the settings
	ctx = settings.SetServiceInfoOnContext(ctx, si)

	serviceMetadata, err := getServiceMetadata(ctx, options.ServerUri)
	if err != nil {
		return ctx, nil, err
	}

	// augment with data received from the metadata endpoint
	si.ServerUri = options.ServerUri
	si.Authority = serviceMetadata.Authority
	si.Audience = serviceMetadata.Audience
	si.ClientAppUri = serviceMetadata.CliAppUri
	si.DataPlaneProxy = serviceMetadata.DataPlaneProxy

	if err := validateServiceInfo(si); err != nil {
		return ctx, nil, err
	}

	if serviceMetadata.Authority != "" {
		useServicePrincipal := options.ServicePrincipal != ""

		var authResult public.AuthResult
		if useServicePrincipal {
			authResult, err = si.performServicePrincipalLogin(ctx)
		} else {
			authResult, err = si.performUserLogin(ctx, options.UseDeviceCode)
		}
		if err != nil {
			return ctx, nil, err
		}

		si.LastToken = authResult.AccessToken
		si.LastTokenExpiry = authResult.ExpiresOn.Unix()

		if !useServicePrincipal {
			si.Principal = authResult.IDToken.PreferredUsername

			// We used the client app URI as the client ID when logging in interactively.
			// This works, but the refresh token will only be valid for the client ID (GUID).
			// So we need to extract the client ID from the access token and use that next time.
			claims := jwt.MapClaims{}
			if _, _, err := jwt.NewParser().ParseUnverified(authResult.AccessToken, claims); err != nil {
				return nil, nil, fmt.Errorf("unable to parse access token: %w", err)
			} else {
				si.ClientId = claims["appid"].(string)
			}
		}
	}

	if options.Persisted {
		err = si.persist()
	}

	return ctx, si, err
}

func normalizeServerUri(uri string) (*url.URL, error) {
	uri = strings.TrimRight(uri, "/")
	parsedUrl, err := url.Parse(uri)
	if err != nil || !parsedUrl.IsAbs() {
		return nil, errors.New("a valid absolute uri is required")
	}

	return parsedUrl, err
}

func Logout() error {
	return (&serviceInfo{}).persist()
}

func (c *serviceInfo) GetAccessToken(ctx context.Context) (string, error) {
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
	if c.CertPath != "" || c.CertThumbprint != "" {
		var err error
		authResult, err = c.performServicePrincipalLogin(ctx)
		if err != nil {
			return "", err
		}
	} else {
		customHttpClient := &clientIdReplacingHttpClient{
			clientAppUri: c.ClientAppUri,
			clientAppId:  c.ClientId,
			innerClient:  httpclient.DefaultRetryableClient.StandardClient(),
		}

		// fall back to using the refresh token from the cache
		client, err := public.New(
			c.ClientAppUri,
			public.WithAuthority(c.Authority),
			public.WithCache(c),
			public.WithHTTPClient(customHttpClient),
		)

		if err != nil {
			return "", err
		}

		accounts, err := client.Accounts(ctx)
		if err != nil {
			return "", fmt.Errorf("unable to get accounts from token cache: %w", err)
		}
		if len(accounts) != 1 {
			return "", errors.New("corrupted token cache")
		}

		authResult, err = client.AcquireTokenSilent(ctx, []string{fmt.Sprintf("%s/%s", c.Audience, userScope)}, public.WithSilentAccount(accounts[0]))
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

func GetPersistedServiceInfo() (settings.ServiceInfo, error) {
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
	if err != nil {
		return nil, err
	}

	if err := validateServiceInfo(si); err != nil {
		return nil, err
	}
	return si, err
}

func validateServiceInfo(si *serviceInfo) error {
	var err error
	if si.ServerUri != "" {
		si.parsedServerUri, err = normalizeServerUri(si.ServerUri)
		if err != nil {
			return fmt.Errorf("the server URI is invalid")
		}
	}
	if si.DataPlaneProxy != "" {
		si.parsedDataPlaneProxy, err = url.Parse(si.DataPlaneProxy)
		if err != nil {
			return fmt.Errorf("the data plane proxy URI is invalid")
		}
	}

	switch si.Proxy {
	case "none", "auto", "automatic", "":
	default:
		si.parsedProxy, err = url.Parse(si.Proxy)
		if err != nil || si.parsedProxy.Host == "" {
			// It may be that the URI was given in the form "host:1234", and the scheme ends up being "host"
			si.parsedProxy, err = url.Parse("http://" + si.Proxy)
			if err != nil {
				return fmt.Errorf("proxy must be 'auto', 'automatic', '' (same as 'auto/automatic'), 'none', or a valid URI")
			}
		}
	}

	return nil
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

func getServiceMetadata(ctx context.Context, serverUri string) (*model.ServiceMetadata, error) {
	req, err := retryablehttp.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/v1/metadata", serverUri), nil)
	if err != nil {
		return nil, fmt.Errorf("unable to create request: %w", err)
	}

	resp, err := httpclient.DefaultRetryableClient.Do(req)
	if err != nil {
		return nil, err
	}
	serviceMetadata := &model.ServiceMetadata{}
	if err := json.NewDecoder(resp.Body).Decode(serviceMetadata); err != nil {
		return nil, errors.New("the server URL does not appear to point to a valid tyger server")
	}

	return serviceMetadata, nil
}

func (si *serviceInfo) performServicePrincipalLogin(ctx context.Context) (authResult confidential.AuthResult, err error) {
	newClient := si.confidentialClient == nil
	if newClient {
		cred, err := si.createServicePrincipalCredential()
		if err != nil {
			return authResult, fmt.Errorf("error creating credential: %w", err)
		}

		client, err := confidential.New(si.Authority, si.Principal, cred, confidential.WithHTTPClient(httpclient.DefaultRetryableClient.StandardClient()))
		if err != nil {
			return authResult, err
		}
		si.confidentialClient = &client
	}

	scopes := []string{fmt.Sprintf("%s/%s", si.Audience, servicePrincipalScope)}
	if !newClient {
		authResult, err = si.confidentialClient.AcquireTokenSilent(ctx, scopes)
	}
	if newClient || err != nil {
		authResult, err = si.confidentialClient.AcquireTokenByCredential(ctx, scopes)
	}

	return authResult, err
}

func (si *serviceInfo) createServicePrincipalCredential() (confidential.Credential, error) {
	if si.CertThumbprint != "" {
		return createCredentialFromSystemCertificateStore(si.CertThumbprint)
	}

	certBytes, err := os.ReadFile(si.CertPath)
	if err != nil {
		return confidential.Credential{}, fmt.Errorf("unable to read certificate file: %w", err)
	}

	certs, privateKey, err := confidential.CertFromPEM(certBytes, "")
	if err != nil {
		return confidential.Credential{}, fmt.Errorf("error decoding certificate: %w", err)
	}

	cred, err := confidential.NewCredFromCert(certs, privateKey)
	if err != nil {
		return confidential.Credential{}, fmt.Errorf("error creating credential: %w", err)
	}

	return cred, nil
}

func (si *serviceInfo) performUserLogin(ctx context.Context, useDeviceCode bool) (authResult public.AuthResult, err error) {
	client, err := public.New(
		si.ClientAppUri,
		public.WithAuthority(si.Authority),
		public.WithCache(si),
		public.WithHTTPClient(httpclient.DefaultRetryableClient.StandardClient()),
	)
	if err != nil {
		return
	}

	scopes := []string{fmt.Sprintf("%s/%s", si.Audience, userScope)}

	if useDeviceCode {
		dc, err := client.AcquireTokenByDeviceCode(ctx, scopes)
		if err != nil {
			return authResult, err
		}

		fmt.Println(dc.Result.Message)
		return dc.AuthenticationResult(ctx)
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

	authResult, err = client.AcquireTokenInteractive(ctx, scopes, public.WithRedirectURI("http://localhost:41087"))
	timer.Stop()
	if err == nil {
		return authResult, err
	}

	var exitError *exec.ExitError
	if errors.Is(err, exec.ErrNotFound) || errors.As(err, &exitError) {
		// this means that we were not able to bring up the browser. Fall back to using the device code flow.
		return si.performUserLogin(ctx, true)
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

type clientIdReplacingHttpClient struct {
	innerClient  *http.Client
	clientAppUri string
	clientAppId  string
}

func (c *clientIdReplacingHttpClient) Do(req *http.Request) (*http.Response, error) {
	if req.Method == http.MethodPost && c.clientAppId != "" && c.clientAppUri != "" {
		// Replace the client_id in the form data with the client ID of the CLI app
		// when POSTing to the token endpoint.
		// We used the client app URI as the client ID when logging in interactively,
		// but AAD requires the client ID when using the refresh token.
		if err := req.ParseForm(); err == nil && req.PostForm != nil {
			if clientId := req.PostForm.Get("client_id"); clientId == c.clientAppUri {
				req.PostForm.Set("client_id", c.clientAppId)
				enc := req.PostForm.Encode()
				req.ContentLength = int64(len(enc))
				req.Body = io.NopCloser(strings.NewReader(enc))
				req.GetBody = func() (io.ReadCloser, error) {
					return io.NopCloser(strings.NewReader(enc)), nil
				}
			}
		}
	}
	return c.innerClient.Do(req)
}

func (c *clientIdReplacingHttpClient) CloseIdleConnections() {
	c.innerClient.CloseIdleConnections()
}
