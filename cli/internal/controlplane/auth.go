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
	"github.com/microsoft/tyger/cli/internal/controlplane/model"
	"github.com/microsoft/tyger/cli/internal/httpclient"
	"github.com/microsoft/tyger/cli/internal/settings"
)

const (
	CacheFileEnvVarName                 = "TYGER_CACHE_FILE"
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
	ServerUri                      string `json:"serverUri"`
	ServicePrincipal               string `json:"servicePrincipal,omitempty"`
	CertificatePath                string `json:"certificatePath,omitempty"`
	CertificateThumbprint          string `json:"certificateThumbprint,omitempty"`
	IgnoreSystemProxySettings      bool   `json:"ignoreSystemProxySettings,omitempty"`
	SkipTlsCertificateVerification bool   `json:"insecureSkipTlsCertificateVerification,omitempty"`
	UseDeviceCode                  bool
	Persisted                      bool
}

type serviceInfo struct {
	settings.Settings
	confidentialClient *confidential.Client
	persisted          bool
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
		Settings: settings.Settings{
			ServerUri:      options.ServerUri,
			Authority:      serviceMetadata.Authority,
			Audience:       serviceMetadata.Audience,
			ClientAppUri:   serviceMetadata.CliAppUri,
			Principal:      options.ServicePrincipal,
			CertPath:       options.CertificatePath,
			CertThumbprint: options.CertificateThumbprint,
			DataPlaneProxy: serviceMetadata.DataPlaneProxy,
		},
		persisted: options.Persisted,
	}

	si.Store(false)

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

			// We used the client app URI as the client ID when logging in interactively.
			// This works, but the refresh token will only be valid for the client ID (GUID).
			// So we need to extract the client ID from the access token and use that next time.
			claims := jwt.MapClaims{}
			if _, _, err := jwt.NewParser().ParseUnverified(authResult.AccessToken, claims); err != nil {
				return nil, fmt.Errorf("unable to parse access token: %w", err)
			} else {
				si.ClientId = claims["appid"].(string)
			}
		}
	}

	err = si.Store(options.Persisted)
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
	return (&serviceInfo{}).Store(true)
}

func (s *serviceInfo) GetAccessToken() (string, error) {
	// Quick check to see if the last token is still valid.
	// This token is in the full MSAL token cache, but unfortunately calling
	// client.AcquireTokenSilent currenly always calls an AAD discovery endpoint, which
	// can take > 0.5 seconds. So we store it ourselves and use it here if it is still valid.
	if s.LastTokenExpiry-discardTokenIfExpiringWithinSeconds > time.Now().UTC().Unix() && s.LastToken != "" {
		return s.LastToken, nil
	}

	if s.Authority == "" {
		return "", nil
	}

	var authResult public.AuthResult
	if s.CertPath != "" || s.CertThumbprint != "" {
		var err error
		authResult, err = s.performServicePrincipalLogin()
		if err != nil {
			return "", err
		}
	} else {
		customHttpClient := &clientIdReplacingHttpClient{
			clientAppUri: s.ClientAppUri,
			clientAppId:  s.ClientId,
			innerClient:  httpclient.DefaultRetryableClient.StandardClient(),
		}

		// fall back to using the refresh token from the cache
		client, err := public.New(
			s.ClientAppUri,
			public.WithAuthority(s.Authority),
			public.WithCache(s),
			public.WithHTTPClient(customHttpClient),
		)

		if err != nil {
			return "", err
		}

		accounts, err := client.Accounts(context.Background())
		if err != nil {
			return "", fmt.Errorf("unable to get accounts from token cache: %w", err)
		}
		if len(accounts) != 1 {
			return "", errors.New("corrupted token cache")
		}

		authResult, err = client.AcquireTokenSilent(context.Background(), []string{fmt.Sprintf("%s/%s", s.Audience, userScope)}, public.WithSilentAccount(accounts[0]))
		if err != nil {
			return "", err
		}
	}

	s.LastToken = authResult.AccessToken
	s.LastTokenExpiry = authResult.ExpiresOn.Unix()

	return authResult.AccessToken, s.Store(s.persisted)
}

func GetPersistedServiceInfo() (*serviceInfo, error) {
	settings, err := settings.LoadSettings()
	if err != nil {
		return nil, err
	}

	return &serviceInfo{Settings: *settings}, nil
}

func getServiceMetadata(serverUri string) (*model.ServiceMetadata, error) {
	resp, err := httpclient.DefaultRetryableClient.Get(fmt.Sprintf("%s/v1/metadata", serverUri))
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
		authResult, err = si.confidentialClient.AcquireTokenSilent(context.Background(), scopes)
	}
	if newClient || err != nil {
		authResult, err = si.confidentialClient.AcquireTokenByCredential(context.Background(), scopes)
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

func (si *serviceInfo) performUserLogin(useDeviceCode bool) (authResult public.AuthResult, err error) {
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
		// this means that we were not able to bring up the browser. Fall back to using the device code flow.
		return si.performUserLogin(true)
	}

	return authResult, err
}

// Implementing the cache.ExportReplace interface to read in the token cache
func (si *serviceInfo) Replace(ctx context.Context, unmarshaler cache.Unmarshaler, hints cache.ReplaceHints) error {
	data, err := base64.StdEncoding.DecodeString(si.FullTokenCache)
	if err == nil {
		unmarshaler.Unmarshal(data)
	}

	return err
}

// Implementing the cache.ExportReplace interface to write out the token cache
func (si *serviceInfo) Export(ctx context.Context, marshaler cache.Marshaler, hints cache.ExportHints) error {
	data, err := marshaler.Marshal()
	if err == nil {
		si.FullTokenCache = base64.StdEncoding.EncodeToString(data)
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
