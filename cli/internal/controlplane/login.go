// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controlplane

import (
	"bytes"
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
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/cache"
	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/confidential"
	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/public"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/microsoft/tyger/cli/internal/client"
	"github.com/microsoft/tyger/cli/internal/controlplane/model"
	"sigs.k8s.io/yaml"
)

const (
	CacheFileEnvVarName                 = "TYGER_CACHE_FILE"
	userScope                           = "Read.Write"
	servicePrincipalScope               = ".default"
	discardTokenIfExpiringWithinSeconds = 10 * 60
	LocalUrlSentinel                    = "local"
	sshConcurrencyLimit                 = 8
	dockerConcurrencyLimit              = 6
	azureTokenExchangeAudience          = "api://AzureADTokenExchange/.default"
)

type AccessToken azcore.AccessToken

type LoginConfig struct {
	ServerUrl                       string `json:"serverUrl"`
	ServicePrincipal                string `json:"servicePrincipal,omitempty"`
	CertificatePath                 string `json:"certificatePath,omitempty"`
	CertificateThumbprint           string `json:"certificateThumbprint,omitempty"`
	ManagedIdentity                 bool   `json:"managedIdentity,omitempty"`
	ManagedIdentityClientId         string `json:"managedIdentityClientId,omitempty"`
	GitHub                          bool   `json:"github,omitempty"`
	TargetFederatedIdentity         string `json:"targetFederatedIdentity,omitempty"`
	Proxy                           string `json:"proxy,omitempty"`
	DisableTlsCertificateValidation bool   `json:"disableTlsCertificateValidation,omitempty"`

	// These are options for tyger-proxy that are ignored here but we don't want unmarshal to fail if present
	Port               int      `json:"port,omitempty"`
	AllowedClientCIDRs []string `json:"allowedClientCIDRs,omitempty"`
	LogPath            string   `json:"logPath,omitempty"`

	UseDeviceCode bool `json:"-"`
	Persisted     bool `json:"-"`
}

// Handle backwards compatibility with `serverUri` instead of `serverUrl`.
func (lc *LoginConfig) UnmarshalJSON(data []byte) error {
	type Alias LoginConfig
	type LoginConfigWithServerUri struct {
		*Alias
		ServerUri string `json:"serverUri,omitempty"`
	}

	augmentedLoginConfig := &LoginConfigWithServerUri{
		Alias: (*Alias)(lc),
	}

	if err := json.Unmarshal(data, augmentedLoginConfig); err != nil {
		return err
	}

	if augmentedLoginConfig.ServerUri != "" {
		if augmentedLoginConfig.ServerUrl != "" && augmentedLoginConfig.ServerUrl != augmentedLoginConfig.ServerUri {
			return fmt.Errorf("conflicting fields: serverUrl=%q and serverUri=%q", augmentedLoginConfig.ServerUrl, augmentedLoginConfig.ServerUri)
		}

		lc.ServerUrl = augmentedLoginConfig.ServerUri
	}

	return nil
}

type serviceInfo struct {
	ServerUrl                       string `json:"serverUrl"`
	parsedServerUrl                 *url.URL
	ClientAppUri                    string `json:"clientAppUri,omitempty"`
	ClientId                        string `json:"clientId,omitempty"`
	LastToken                       string `json:"lastToken,omitempty"`
	LastTokenExpiry                 int64  `json:"lastTokenExpiration,omitempty"`
	Principal                       string `json:"principal,omitempty"`
	ManagedIdentity                 bool   `json:"managedIdentity,omitempty"`
	ManagedIdentityClientId         string `json:"managedIdentityClientId,omitempty"`
	GitHub                          bool   `json:"github,omitempty"`
	TargetFederatedIdentity         string `json:"targetFederatedIdentity,omitempty"`
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

// Handle backwards compatibility with `serverUri` instead of `serverUrl`.
func (si *serviceInfo) UnmarshalJSON(data []byte) error {
	type Alias serviceInfo
	type ServiceInfoWithServerUri struct {
		*Alias
		ServerUri string `json:"serverUri,omitempty"`
	}

	augmentedServiceInfo := &ServiceInfoWithServerUri{
		Alias: (*Alias)(si),
	}

	if err := json.Unmarshal(data, augmentedServiceInfo); err != nil {
		return err
	}

	if augmentedServiceInfo.ServerUri != "" {
		if augmentedServiceInfo.ServerUrl != "" && augmentedServiceInfo.ServerUrl != augmentedServiceInfo.ServerUri {
			return fmt.Errorf("conflicting fields: serverUrl=%q and serverUri=%q", augmentedServiceInfo.ServerUrl, augmentedServiceInfo.ServerUri)
		}

		si.ServerUrl = augmentedServiceInfo.ServerUri
	}

	return nil
}

func Login(ctx context.Context, options LoginConfig) (*client.TygerClient, error) {
	if options.ServerUrl == LocalUrlSentinel {
		optionsClone := options
		optionsClone.ServerUrl = client.GetDefaultSocketUrl()
		c, errUnix := Login(ctx, optionsClone)
		if errUnix == nil {
			return c, nil
		}

		optionsClone.ServerUrl = "docker://"
		c, errDocker := Login(ctx, optionsClone)
		if errDocker == nil {
			return c, nil
		}

		return nil, errUnix
	}

	normalizedServerUrl, err := NormalizeServerUrl(options.ServerUrl)
	if err != nil {
		return nil, err
	}
	options.ServerUrl = normalizedServerUrl.String()

	si := &serviceInfo{
		ServerUrl:                       options.ServerUrl,
		parsedServerUrl:                 normalizedServerUrl,
		Principal:                       options.ServicePrincipal,
		CertPath:                        options.CertificatePath,
		CertThumbprint:                  options.CertificateThumbprint,
		ManagedIdentity:                 options.ManagedIdentity,
		ManagedIdentityClientId:         options.ManagedIdentityClientId,
		TargetFederatedIdentity:         options.TargetFederatedIdentity,
		Proxy:                           options.Proxy,
		DisableTlsCertificateValidation: options.DisableTlsCertificateValidation,
	}

	defaultClientOptions := client.ClientOptions{
		ProxyString:                     options.Proxy,
		DisableTlsCertificateValidation: options.DisableTlsCertificateValidation,
	}

	if err := client.SetDefaultNetworkClientSettings(&defaultClientOptions); err != nil {
		return nil, err
	}

	var tygerClient *client.TygerClient
	if normalizedServerUrl.Scheme == "docker" {
		dockerParams, err := client.ParseDockerUrl(normalizedServerUrl)
		if err != nil {
			return nil, fmt.Errorf("invalid Docker URL: %w", err)
		}

		loginCommand := exec.CommandContext(ctx, "docker", dockerParams.FormatLoginArgs()...)
		var outb, errb bytes.Buffer
		loginCommand.Stdout = &outb
		loginCommand.Stderr = &errb
		if err := loginCommand.Run(); err != nil {
			return nil, fmt.Errorf("failed to establish a tyger connection: %w. stderr: %s", err, errb.String())
		}

		socketUrl, err := NormalizeServerUrl(outb.String())
		if err != nil {
			return nil, fmt.Errorf("failed to parse socket URL: %w", err)
		}

		if socketUrl.Scheme != "http+unix" {
			panic(fmt.Sprintf("unexpected scheme: %s", socketUrl.Scheme))
		}

		dockerParams.SocketPath = strings.Split(socketUrl.Path, ":")[0]
		si.parsedServerUrl = dockerParams.URL()
		si.ServerUrl = si.parsedServerUrl.String()

		controlPlaneClientOptions := defaultClientOptions // clone
		controlPlaneClientOptions.ProxyString = "none"
		controlPlaneClientOptions.CreateTransport = client.MakeCommandTransport(dockerConcurrencyLimit, "docker", dockerParams.FormatCmdLine()...)
		controlPlaneClient, err := client.NewControlPlaneClient(&controlPlaneClientOptions)
		if err != nil {
			return nil, fmt.Errorf("unable to create control plane client: %w", err)
		}

		dataPlaneClientOptions := controlPlaneClientOptions
		dataPlaneClientOptions.DisableRetries = true
		dataPlaneClient, err := client.NewDataPlaneClient(&dataPlaneClientOptions)
		if err != nil {
			return nil, fmt.Errorf("unable to create data plane client: %w", err)
		}

		tygerClient = &client.TygerClient{
			ControlPlaneUrl:    socketUrl,
			ControlPlaneClient: controlPlaneClient,
			GetAccessToken:     si.GetAccessToken,
			DataPlaneClient:    dataPlaneClient,
			Principal:          si.Principal,
			RawControlPlaneUrl: si.parsedServerUrl,
			RawProxy:           si.parsedProxy,
		}
	} else if normalizedServerUrl.Scheme == "ssh" {
		sshParams, err := client.ParseSshUrl(normalizedServerUrl)
		if err != nil {
			return nil, fmt.Errorf("invalid ssh URL: %w", err)
		}

		// Give the user a chance accept remote host key if necessary
		preFlightCommand := exec.CommandContext(ctx, "ssh", sshParams.FormatLoginArgs("--preflight")...)
		preFlightCommand.Stdin = os.Stdin
		preFlightCommand.Stdout = os.Stdout
		preFlightCommand.Stderr = os.Stderr
		if err := preFlightCommand.Run(); err != nil {
			return nil, fmt.Errorf("failed to establish a remote tyger connection: %w", err)
		}

		loginCommand := exec.CommandContext(ctx, "ssh", sshParams.FormatLoginArgs()...)
		var outb, errb bytes.Buffer
		loginCommand.Stdout = &outb
		loginCommand.Stderr = &errb
		if err := loginCommand.Run(); err != nil {
			return nil, fmt.Errorf("failed to establish a remote tyger connection: %w. stderr: %s", err, errb.String())
		}

		socketUrl, err := NormalizeServerUrl(outb.String())
		if err != nil {
			return nil, fmt.Errorf("failed to parse socket URL: %w", err)
		}

		if socketUrl.Scheme != "http+unix" {
			panic(fmt.Sprintf("unexpected scheme: %s", socketUrl.Scheme))
		}

		sshParams.SocketPath = strings.Split(socketUrl.Path, ":")[0]
		si.parsedServerUrl = sshParams.URL()
		si.ServerUrl = si.parsedServerUrl.String()

		controlPlaneOptions := defaultClientOptions // clone
		controlPlaneOptions.ProxyString = "none"
		controlPlaneOptions.CreateTransport = client.MakeCommandTransport(sshConcurrencyLimit, "ssh", sshParams.FormatCmdLine()...)
		controlPlaneClient, err := client.NewControlPlaneClient(&controlPlaneOptions)
		if err != nil {
			return nil, fmt.Errorf("unable to create control plane client: %w", err)
		}

		dataPlaneOptions := controlPlaneOptions // clone
		dataPlaneOptions.CreateTransport = client.MakeCommandTransport(sshConcurrencyLimit, "ssh", sshParams.FormatDataPlaneCmdLine()...)

		dataPlaneClient, err := client.NewDataPlaneClient(&dataPlaneOptions)
		if err != nil {
			return nil, fmt.Errorf("unable to create data plane client: %w", err)
		}

		tygerClient = &client.TygerClient{
			ControlPlaneUrl:    socketUrl,
			ControlPlaneClient: controlPlaneClient,
			GetAccessToken:     si.GetAccessToken,
			DataPlaneClient:    dataPlaneClient,
			Principal:          si.Principal,
			RawControlPlaneUrl: si.parsedServerUrl,
			RawProxy:           si.parsedProxy,
		}
	} else {
		if err := validateServiceInfo(si); err != nil {
			return nil, err
		}

		serviceMetadata, err := GetServiceMetadata(ctx, options.ServerUrl)
		if err != nil {
			return nil, err
		}

		// augment with data received from the metadata endpoint
		si.ServerUrl = options.ServerUrl
		si.Authority = serviceMetadata.Authority
		si.Audience = serviceMetadata.Audience
		si.ClientId = serviceMetadata.CliAppId
		si.ClientAppUri = serviceMetadata.CliAppUri
		si.DataPlaneProxy = serviceMetadata.DataPlaneProxy

		if err := validateServiceInfo(si); err != nil {
			return nil, err
		}

		if serviceMetadata.Authority != "" {
			var authResult AccessToken
			if options.ServicePrincipal != "" {
				authResult, err = si.performServicePrincipalLogin(ctx)
			} else if options.ManagedIdentity {
				authResult, si.Principal, err = si.performManagedIdentityLogin(ctx, options.ManagedIdentityClientId, options.TargetFederatedIdentity)
			} else if options.GitHub {
				authResult, si.Principal, err = si.performGitHubLogin(ctx, options.TargetFederatedIdentity)
			} else {
				authResult, si.Principal, err = si.performUserLogin(ctx, options.UseDeviceCode)
				if si.ClientId == "" {
					// Earlier serrver versions did not publish the client app ID in the metadata endpoint and used
					// the client app URI as the client ID.
					// We used the client app URI as the client ID when logging in interactively.
					// This works, but the refresh token will only be valid for the client ID (GUID).
					// So we need to extract the client ID from the access token and use that next time.
					claims := jwt.MapClaims{}
					if _, _, err := jwt.NewParser().ParseUnverified(authResult.Token, claims); err != nil {
						return nil, fmt.Errorf("unable to parse access token: %w", err)
					} else {
						var ok bool
						si.ClientId, ok = claims["appid"].(string)
						if !ok {
							return nil, errors.New("unable to extract client ID from access token; the client is not compatible with this version of the CLI")
						}
					}
				}
			}

			if err != nil {
				return nil, err
			}

			si.LastToken = authResult.Token
			si.LastTokenExpiry = authResult.ExpiresOn.Unix()
		}

		controlPlaneOptions := defaultClientOptions

		dataPlaneOptions := defaultClientOptions
		if si.DataPlaneProxy != "" {
			dataPlaneOptions.ProxyString = si.DataPlaneProxy
		}

		switch si.parsedServerUrl.Scheme {
		case "http+unix", "https+unix":
			controlPlaneOptions.DisableRetries = true
			dataPlaneOptions.DisableRetries = true
		case "http", "https":
		default:
			panic(fmt.Sprintf("unhandled scheme: %s", si.parsedServerUrl.Scheme))
		}

		cpClient, err := client.NewControlPlaneClient(&controlPlaneOptions)
		if err != nil {
			return nil, fmt.Errorf("unable to create control plane client: %w", err)
		}

		dpClient, err := client.NewDataPlaneClient(&dataPlaneOptions)
		if err != nil {
			return nil, fmt.Errorf("unable to create data plane client: %w", err)
		}

		tygerClient = &client.TygerClient{
			ControlPlaneUrl:    si.parsedServerUrl,
			ControlPlaneClient: cpClient,
			GetAccessToken:     si.GetAccessToken,
			DataPlaneClient:    dpClient,
			Principal:          si.Principal,
			RawControlPlaneUrl: si.parsedServerUrl,
			RawProxy:           si.parsedProxy,
		}

		if serviceMetadata.RbacEnabled {
			if roles, err := tygerClient.GetRoles(ctx); err == nil && len(roles) == 0 {
				return nil, errors.New("the principal does not have any assigned roles")
			}
		}
	}

	if options.Persisted {
		err = si.persist()
		if err != nil {
			return nil, err
		}
	}

	return tygerClient, nil
}

func NormalizeServerUrl(serverUrl string) (*url.URL, error) {
	serverUrl = strings.TrimRight(serverUrl, "/")
	parsedUrl, err := url.Parse(serverUrl)
	if err != nil || !parsedUrl.IsAbs() {
		return nil, errors.New("a valid absolute URL is required")
	}

	if parsedUrl.Scheme == "unix" {
		parsedUrl.Scheme = "http+unix"
	}

	if parsedUrl.Scheme == "http+unix" || parsedUrl.Scheme == "https+unix" {
		// Turn a relative path into an absolute path
		// If the path is already absolute, Host will be empty
		// Otherwise, host will be the first path segment.
		if parsedUrl.Host != "" {
			if parsedUrl.Path == "" {
				parsedUrl.Path = parsedUrl.Host
			} else {
				parsedUrl.Path = parsedUrl.Host + parsedUrl.Path
			}
			parsedUrl.Host = ""
		}

		if !path.IsAbs(parsedUrl.Path) {
			parsedUrl.Path, err = filepath.Abs(parsedUrl.Path)
			if err != nil {
				return nil, fmt.Errorf("failed to make path absolute: %w", err)
			}
		}

		if !strings.HasSuffix(parsedUrl.Path, ":") {
			parsedUrl.Path += ":"
		}
	}

	return parsedUrl, err
}

func Logout() error {
	return (&serviceInfo{}).persist()
}

func (c *serviceInfo) GetAccessToken(ctx context.Context) (string, error) {
	// Quick check to see if the last token is still valid.
	// This token is in the full MSAL token cache, but unfortunately calling
	// client.AcquireTokenSilent currently always calls an AAD discovery endpoint, which
	// can take > 0.5 seconds. So we store it ourselves and use it here if it is still valid.
	if c.LastTokenExpiry-discardTokenIfExpiringWithinSeconds > time.Now().UTC().Unix() && c.LastToken != "" {
		return c.LastToken, nil
	}

	if c.Authority == "" {
		return "", nil
	}

	var accessToken AccessToken
	if c.CertPath != "" || c.CertThumbprint != "" {
		var err error
		accessToken, err = c.performServicePrincipalLogin(ctx)
		if err != nil {
			return "", err
		}
	} else if c.ManagedIdentity {
		var err error
		accessToken, _, err = c.performManagedIdentityLogin(ctx, c.ManagedIdentityClientId, c.TargetFederatedIdentity)
		if err != nil {
			return "", err
		}
	} else if c.GitHub {
		var err error
		accessToken, _, err = c.performGitHubLogin(ctx, c.TargetFederatedIdentity)
		if err != nil {
			return "", err
		}
	} else {
		customHttpClient := &clientIdReplacingHttpClient{
			clientAppUri: c.ClientAppUri,
			clientAppId:  c.ClientId,
			innerClient:  http.DefaultClient,
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

		authResult, err := client.AcquireTokenSilent(ctx, []string{fmt.Sprintf("%s/%s", c.Audience, userScope)}, public.WithSilentAccount(accounts[0]))
		if err != nil {
			return "", err
		}

		accessToken = AccessToken{
			Token:     authResult.AccessToken,
			ExpiresOn: authResult.ExpiresOn,
		}
	}

	c.LastToken = accessToken.Token
	c.LastTokenExpiry = accessToken.ExpiresOn.Unix()

	return accessToken.Token, c.persist()
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

func GetClientFromCache() (*client.TygerClient, error) {
	si, err := readCachedServiceInfo()
	if err != nil {
		return nil, err
	}

	if si.parsedServerUrl == nil {
		return nil, errors.New("not logged in")
	}

	defaultClientOptions := client.ClientOptions{
		ProxyString:                     si.Proxy,
		DisableTlsCertificateValidation: si.DisableTlsCertificateValidation,
	}

	if err := client.SetDefaultNetworkClientSettings(&defaultClientOptions); err != nil {
		return nil, err
	}

	if si.parsedServerUrl.Scheme == "docker" {
		dockerParams, err := client.ParseDockerUrl(si.parsedServerUrl)
		if err != nil {
			return nil, fmt.Errorf("invalid ssh URL: %w", err)
		}

		controlPlaneClientOptions := defaultClientOptions
		controlPlaneClientOptions.ProxyString = "none"
		controlPlaneClientOptions.CreateTransport = client.MakeCommandTransport(dockerConcurrencyLimit, "docker", dockerParams.FormatCmdLine()...)
		cpClient, err := client.NewClient(&controlPlaneClientOptions)
		if err != nil {
			return nil, fmt.Errorf("unable to create control plane client: %w", err)
		}

		dataPlaneClientOptions := controlPlaneClientOptions // clone
		dataPlaneClientOptions.DisableRetries = true

		dpClient, err := client.NewDataPlaneClient(&dataPlaneClientOptions)
		if err != nil {
			return nil, fmt.Errorf("unable to create data plane client: %w", err)
		}

		endpoint := url.URL{Scheme: "http+unix", Path: fmt.Sprintf("%s:", dockerParams.SocketPath)}

		return &client.TygerClient{
			ControlPlaneUrl:    &endpoint,
			ControlPlaneClient: cpClient,
			GetAccessToken:     si.GetAccessToken,
			DataPlaneClient:    dpClient,
			Principal:          si.Principal,
			RawControlPlaneUrl: si.parsedServerUrl,
			RawProxy:           si.parsedProxy,
		}, nil
	} else if si.parsedServerUrl.Scheme == "ssh" {
		sshParams, err := client.ParseSshUrl(si.parsedServerUrl)
		if err != nil {
			return nil, fmt.Errorf("invalid ssh URL: %w", err)
		}

		controlPlaneOptions := defaultClientOptions
		controlPlaneOptions.ProxyString = "none"
		controlPlaneOptions.CreateTransport = client.MakeCommandTransport(sshConcurrencyLimit, "ssh", sshParams.FormatCmdLine()...)

		dataPlaneClientOptions := controlPlaneOptions // clone
		dataPlaneClientOptions.CreateTransport = client.MakeCommandTransport(sshConcurrencyLimit, "ssh", sshParams.FormatDataPlaneCmdLine()...)

		cpClient, err := client.NewClient(&controlPlaneOptions)
		if err != nil {
			return nil, fmt.Errorf("unable to create control plane client: %w", err)
		}

		dpClient, err := client.NewDataPlaneClient(&dataPlaneClientOptions)
		if err != nil {
			return nil, fmt.Errorf("unable to create data plane client: %w", err)
		}

		endpoint := url.URL{Scheme: "http+unix", Path: fmt.Sprintf("%s:", sshParams.SocketPath)}

		return &client.TygerClient{
			ControlPlaneUrl:    &endpoint,
			ControlPlaneClient: cpClient,
			GetAccessToken:     si.GetAccessToken,
			DataPlaneClient:    dpClient,
			Principal:          si.Principal,
			RawControlPlaneUrl: si.parsedServerUrl,
			RawProxy:           si.parsedProxy,
		}, nil
	} else {

		controlPlaneOptions := defaultClientOptions

		dataPlaneOptions := defaultClientOptions
		if si.DataPlaneProxy != "" {
			dataPlaneOptions.ProxyString = si.DataPlaneProxy
		}

		switch si.parsedServerUrl.Scheme {
		case "http+unix", "https+unix":
			controlPlaneOptions.DisableRetries = true
			dataPlaneOptions.DisableRetries = true
		case "http", "https":
		}

		controlPlaneClient, err := client.NewClient(&controlPlaneOptions)
		if err != nil {
			return nil, fmt.Errorf("unable to create control plane client: %w", err)
		}

		dataPlaneClient, err := client.NewDataPlaneClient(&dataPlaneOptions)
		if err != nil {
			return nil, fmt.Errorf("unable to create data plane client: %w", err)
		}

		return &client.TygerClient{
			ControlPlaneUrl:    si.parsedServerUrl,
			ControlPlaneClient: controlPlaneClient,
			GetAccessToken:     si.GetAccessToken,
			DataPlaneClient:    dataPlaneClient,
			Principal:          si.Principal,
			RawControlPlaneUrl: si.parsedServerUrl,
			RawProxy:           si.parsedProxy,
		}, nil
	}
}

func validateServiceInfo(si *serviceInfo) error {
	var err error
	if si.ServerUrl != "" {
		si.parsedServerUrl, err = NormalizeServerUrl(si.ServerUrl)
		if err != nil {
			return fmt.Errorf("the server URL is invalid")
		}
	}
	if si.DataPlaneProxy != "" {
		si.parsedDataPlaneProxy, err = url.Parse(si.DataPlaneProxy)
		if err != nil {
			return fmt.Errorf("the data plane proxy URL is invalid")
		}
	}

	switch si.Proxy {
	case "none", "auto", "automatic", "":
	default:
		si.parsedProxy, err = url.Parse(si.Proxy)
		if err != nil || si.parsedProxy.Host == "" {
			// It may be that the URL was given in the form "host:1234", and the scheme ends up being "host"
			si.parsedProxy, err = url.Parse("http://" + si.Proxy)
			if err != nil {
				return fmt.Errorf("proxy must be 'auto', 'automatic', '' (same as 'auto/automatic'), 'none', or a valid URL")
			}
		}
	}

	return nil
}

func readCachedServiceInfo() (*serviceInfo, error) {
	path, err := GetCachePath()
	if err != nil {
		return nil, err
	}

	var bytes []byte
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

	if err != nil {
		return nil, err
	}

	si := serviceInfo{}
	err = yaml.Unmarshal(bytes, &si)
	if err != nil {
		return nil, err
	}

	if err := validateServiceInfo(&si); err != nil {
		return nil, err
	}

	return &si, nil
}

func GetServiceMetadata(ctx context.Context, serverUrl string) (*model.ServiceMetadata, error) {
	// Not using a retryable client because when doing `tyger login --local` we first try to use the unix socket
	// before trying the docker gateway and we don't want to wait for retries.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/metadata", serverUrl), nil)
	if err != nil {
		return nil, fmt.Errorf("unable to create request: %w", err)
	}

	resp, err := client.DefaultClient.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	serviceMetadata := &model.ServiceMetadata{}
	if err := json.NewDecoder(resp.Body).Decode(serviceMetadata); err != nil {
		// Check if the server is older than the client (uses the old `/v1/` path)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/v1/metadata", serverUrl), nil)
		if err != nil {
			return nil, fmt.Errorf("unable to create request: %w", err)
		}

		resp, err := client.DefaultClient.HTTPClient.Do(req)
		if err != nil {
			return nil, err
		}
		serviceMetadata := &model.ServiceMetadata{}
		if err := json.NewDecoder(resp.Body).Decode(serviceMetadata); err != nil {
			return nil, errors.New("the server URL does not appear to point to a valid tyger server")
		}
		return nil, errors.New("the server hosts an older version of tyger that is not compatibile with this client")
	}

	return serviceMetadata, nil
}

func (si *serviceInfo) performManagedIdentityLogin(ctx context.Context, managedIdentityId string, targetFederatedIdentity string) (AccessToken, string, error) {
	opt := azidentity.ManagedIdentityCredentialOptions{}
	if managedIdentityId != "" {
		opt.ID = azidentity.ClientID(managedIdentityId)
	}
	cred, err := azidentity.NewManagedIdentityCredential(&opt)
	if err != nil {
		return AccessToken{}, "", fmt.Errorf("error creating managed identity credential: %w", err)
	}

	var scopes []string
	if targetFederatedIdentity != "" {
		scopes = []string{azureTokenExchangeAudience}
	} else {
		scopes = []string{si.getApiServerScope()}
	}

	tok, err := cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: scopes})
	if err != nil {
		return AccessToken{}, "", err
	}

	var principal string
	claims := jwt.MapClaims{}
	if _, _, err := jwt.NewParser().ParseUnverified(tok.Token, claims); err == nil {
		principal, _ = claims["azp"].(string)
	}

	if targetFederatedIdentity == "" {
		return AccessToken(tok), principal, nil
	}

	return si.performFederatedIdentityLogin(ctx, tok.Token, targetFederatedIdentity)
}

func (si *serviceInfo) performGitHubLogin(ctx context.Context, targetFederatedIdentity string) (AccessToken, string, error) {
	requestUrl := os.Getenv("ACTIONS_ID_TOKEN_REQUEST_URL")
	requestToken := os.Getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN")

	if requestUrl == "" || requestToken == "" {
		return AccessToken{}, "", errors.New("environment variables ACTIONS_ID_TOKEN_REQUEST_URL or ACTIONS_ID_TOKEN_REQUEST_TOKEN are not set")
	}

	parsedRequestUrl, err := url.Parse(requestUrl)
	if err != nil {
		return AccessToken{}, "", fmt.Errorf("invalid ACTIONS_ID_TOKEN_REQUEST_URL environment variable: %w", err)
	}

	query := parsedRequestUrl.Query()
	query.Set("audience", azureTokenExchangeAudience)
	parsedRequestUrl.RawQuery = query.Encode()
	requestUrl = parsedRequestUrl.String()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestUrl, nil)
	if err != nil {
		return AccessToken{}, "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", requestToken))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return AccessToken{}, "", fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return AccessToken{}, "", fmt.Errorf("failed to get OIDC token, status code: %d", resp.StatusCode)
	}

	var response struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return AccessToken{}, "", fmt.Errorf("failed to decode response: %w", err)
	}

	return si.performFederatedIdentityLogin(ctx, response.Value, targetFederatedIdentity)
}

func (si *serviceInfo) performFederatedIdentityLogin(ctx context.Context, token string, targetFederatedIdentity string) (AccessToken, string, error) {
	assertionCred := confidential.NewCredFromAssertionCallback(func(ctx context.Context, aro confidential.AssertionRequestOptions) (string, error) {
		return token, nil
	})

	confidentialClient, err := confidential.New(si.Authority, targetFederatedIdentity, assertionCred, confidential.WithHTTPClient(client.DefaultClient.StandardClient()))
	if err != nil {
		return AccessToken{}, "", fmt.Errorf("error creating confidential client: %w", err)
	}
	authResult, err := confidentialClient.AcquireTokenByCredential(ctx, []string{si.getApiServerScope()})
	if err != nil {
		return AccessToken{}, "", fmt.Errorf("error performing token exchange: %w", err)
	}

	return AccessToken{
		Token:     authResult.AccessToken,
		ExpiresOn: authResult.ExpiresOn,
	}, targetFederatedIdentity, nil
}

func (si *serviceInfo) performServicePrincipalLogin(ctx context.Context) (AccessToken, error) {
	newClient := si.confidentialClient == nil
	if newClient {
		cred, err := si.createServicePrincipalCredential()
		if err != nil {
			return AccessToken{}, fmt.Errorf("error creating credential: %w", err)
		}

		client, err := confidential.New(si.Authority, si.Principal, cred, confidential.WithHTTPClient(client.DefaultClient.StandardClient()))
		if err != nil {
			return AccessToken{}, err
		}
		si.confidentialClient = &client
	}

	var authResult confidential.AuthResult
	var err error
	scopes := []string{fmt.Sprintf("%s/%s", si.Audience, servicePrincipalScope)}
	if !newClient {
		authResult, err = si.confidentialClient.AcquireTokenSilent(ctx, scopes)
	}
	if newClient || err != nil {
		authResult, err = si.confidentialClient.AcquireTokenByCredential(ctx, scopes)
	}

	return AccessToken{
		Token:     authResult.AccessToken,
		ExpiresOn: authResult.ExpiresOn,
	}, err
}

func (si *serviceInfo) getApiServerScope() string {
	return fmt.Sprintf("%s/%s", si.Audience, servicePrincipalScope)
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

func (si *serviceInfo) performUserLogin(ctx context.Context, useDeviceCode bool) (AccessToken, string, error) {
	clientId := si.ClientId
	if clientId == "" {
		clientId = si.ClientAppUri
	}
	client, err := public.New(
		clientId,
		public.WithAuthority(si.Authority),
		public.WithCache(si),
		public.WithHTTPClient(client.DefaultClient.StandardClient()),
	)
	if err != nil {
		return AccessToken{}, "", err
	}

	scopes := []string{fmt.Sprintf("%s/%s", si.Audience, userScope)}

	if useDeviceCode {
		dc, err := client.AcquireTokenByDeviceCode(ctx, scopes)
		if err != nil {
			return AccessToken{}, "", err
		}

		fmt.Println(dc.Result.Message)
		authResult, err := dc.AuthenticationResult(ctx)

		at := AccessToken{
			Token:     authResult.AccessToken,
			ExpiresOn: authResult.ExpiresOn,
		}

		return at, authResult.IDToken.PreferredUsername, err
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

	authResult, err := client.AcquireTokenInteractive(ctx, scopes, public.WithRedirectURI("http://localhost:41087"))
	timer.Stop()
	if err == nil {
		at := AccessToken{
			Token:     authResult.AccessToken,
			ExpiresOn: authResult.ExpiresOn,
		}
		return at, authResult.IDToken.PreferredUsername, nil
	}

	var exitError *exec.ExitError
	if errors.Is(err, exec.ErrNotFound) || errors.As(err, &exitError) {
		// this means that we were not able to bring up the browser. Fall back to using the device code flow.
		return si.performUserLogin(ctx, true)
	}

	return AccessToken{}, "", err
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
