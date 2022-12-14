package clicontext

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/model"
	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/cache"
	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/confidential"
	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/public"
	"github.com/google/uuid"
	"github.com/hashicorp/go-retryablehttp"
	"gopkg.in/yaml.v3"
)

const (
	cliClientId                         = "d81fc78a-b30f-49ee-8697-54775895e218" // this needs to be the app ID and not the identifier URI, otherwise the refresh tokens will not work.
	userScope                           = "Read.Write"
	servicePrincipalScope               = ".default"
	discardTokenIfExpiringWithinSeconds = 10 * 60
)

type CliContext interface {
	GetServerUri() string
	GetPrincipal() string
	GetAccessToken() (string, error)
	Validate() error
}

type LoginOptions struct {
	ServerUri        string
	ServicePrincipal string
	CertificatePath  string
	UseDeviceCode    bool
}

type cliContext struct {
	ServerUri       string `yaml:"serverUri"`
	NoAuth          bool   `yaml:"noAuth,omitempty"`
	LastToken       string `yaml:"lastToken,omitempty"`
	LastTokenExpiry int64  `yaml:"lastTokenExpiration,omitempty"`
	Principal       string `yaml:"principal,omitempty"`
	CertPath        string `yaml:"certPath,omitempty"`
	Authority       string `yaml:"authority,omitempty"`
	Audience        string `yaml:"audience,omitempty"`
	FullCache       string `yaml:"fullCache,omitempty"`
}

func (c *cliContext) GetServerUri() string {
	return c.ServerUri
}

func (c *cliContext) GetPrincipal() string {
	return c.Principal
}

func Login(options LoginOptions) error {
	normalizedServerUri, err := normalizeServerUri(options.ServerUri)
	if err != nil {
		return err
	}
	options.ServerUri = normalizedServerUri
	serviceMetadata, err := getServiceMetadata(options.ServerUri)
	if err != nil {
		return err
	}

	ctx := &cliContext{
		ServerUri: options.ServerUri,
		Authority: serviceMetadata.Authority,
		Audience:  serviceMetadata.Audience,
		Principal: options.ServicePrincipal,
		CertPath:  options.CertificatePath,
	}

	if serviceMetadata.Authority != "" {
		useServicePrincipal := options.ServicePrincipal != ""

		var authResult public.AuthResult
		if useServicePrincipal {
			authResult, err = ctx.performServicePrincipalLogin()
		} else {
			authResult, err = ctx.performUserLogin(options.UseDeviceCode)
		}
		if err != nil {
			return err
		}

		ctx.LastToken = authResult.AccessToken
		ctx.LastTokenExpiry = authResult.ExpiresOn.Unix()

		if !useServicePrincipal {
			ctx.Principal = authResult.IDToken.PreferredUsername
		}
	}

	return ctx.writeCliContext()
}

func NewRetryableClient() *http.Client {
	client := retryablehttp.NewClient()
	client.Logger = nil
	client.RetryMax = 6
	client.ErrorHandler = func(resp *http.Response, err error, numTries int) (*http.Response, error) {
		return resp, err
	}
	return client.StandardClient()
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
	return (&cliContext{}).writeCliContext()
}

func (c *cliContext) GetAccessToken() (string, error) {
	// Quick check to see if the last token is still valid.
	// This token is in the full MSAL token cache, but unfortunately calling
	// client.AcquireTokenSilent currenly always calls an AAD discovery endpoint, which
	// can take > 0.5 seconds. So we store it ourselves and use it here if it is still valid.
	if c.LastTokenExpiry-discardTokenIfExpiringWithinSeconds > time.Now().UTC().Unix() && c.LastToken != "" {
		return c.LastToken, nil
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

		accounts := client.Accounts()
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

	return authResult.AccessToken, c.writeCliContext()
}

func contextPath() (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("unable to locate cache directory: %v", err)
	}
	tygerDir := filepath.Join(cacheDir, "tyger")
	err = os.MkdirAll(tygerDir, 0775)
	if err != nil {
		return "", fmt.Errorf("unable to create %s directory", tygerDir)
	}
	return filepath.Join(tygerDir, ".tyger"), nil
}

func (context *cliContext) writeCliContext() error {
	path, err := contextPath()
	if err == nil {
		var bytes []byte
		bytes, err = yaml.Marshal(context)
		if err == nil {
			err = writeCliContextContents(path, bytes)
		}
	}

	if err != nil {
		return fmt.Errorf("failed to write context: %v", err)
	}

	return nil
}

func writeCliContextContents(path string, bytes []byte) error {
	// Write to a temp file in the same directory first
	tempFileName := fmt.Sprintf("%s.%v", path, uuid.New())
	defer os.Remove(tempFileName)
	if err := ioutil.WriteFile(tempFileName, bytes, 0600); err != nil {
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

func GetCliContext() (CliContext, error) {
	context := &cliContext{}
	path, err := contextPath()
	if err != nil {
		return context, err
	}

	bytes, err := getCliContextContents(path)

	if err != nil {
		return context, err
	}

	err = yaml.Unmarshal(bytes, &context)
	return context, err
}

func getCliContextContents(path string) ([]byte, error) {
	var bytes []byte
	var err error

	// If the file is not readable due to a permission error,
	// it could be because another process is holding the file open.
	// In that case, we retry over a short period of time.
	for i := 0; i < 50; i++ {
		if i > 0 {
			time.Sleep(100 * time.Millisecond)
		}

		bytes, err = ioutil.ReadFile(path)
		if err == nil || !errors.Is(err, os.ErrPermission) {
			break
		}
	}

	return bytes, err
}

func (c *cliContext) Validate() error {
	if c.ServerUri != "" {
		_, err := c.GetAccessToken()
		if err == nil {
			return nil
		}
		return fmt.Errorf("run tyger login: %v", err)
	}

	return errors.New("run tyger login")
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

func (ctx *cliContext) performServicePrincipalLogin() (authResult confidential.AuthResult, err error) {
	certBytes, err := ioutil.ReadFile(ctx.CertPath)
	if err != nil {
		return authResult, fmt.Errorf("unable to read certificate file: %v", err)
	}

	certs, privateKey, err := confidential.CertFromPEM(certBytes, "")
	if err != nil {
		return authResult, fmt.Errorf("error decoding certificate: %v", err)
	}
	if len(certs) != 1 {
		return authResult, errors.New("there should be only one certifiate in the PEM file")
	}
	client, err := confidential.New(ctx.Principal, confidential.NewCredFromCert(certs[0], privateKey), confidential.WithAuthority(ctx.Authority), confidential.WithAccessor(ctx))
	if err != nil {
		return authResult, err
	}

	scopes := []string{fmt.Sprintf("%s/%s", ctx.Audience, servicePrincipalScope)}
	authResult, err = client.AcquireTokenByCredential(context.Background(), scopes)
	return
}

func (ctx *cliContext) performUserLogin(useDeviceCode bool) (authResult public.AuthResult, err error) {
	client, err := public.New(
		cliClientId,
		public.WithAuthority(ctx.Authority),
		public.WithCache(ctx),
	)
	if err != nil {
		return
	}

	scopes := []string{fmt.Sprintf("%s/%s", ctx.Audience, userScope)}

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

				if err = ioutil.WriteFile(shimPath, []byte(shimContents), 0700); err == nil {
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
		return ctx.performUserLogin(true)
	}

	return authResult, err
}

// Implementing the cache.ExportReplace interface to read in the token cache
func (c *cliContext) Replace(cache cache.Unmarshaler, key string) {
	if data, err := base64.StdEncoding.DecodeString(c.FullCache); err == nil {
		cache.Unmarshal(data)
	}
}

// Implementing the cache.ExportReplace interface to write out the token cache
func (t *cliContext) Export(cache cache.Marshaler, key string) {
	data, err := cache.Marshal()
	if err == nil {
		t.FullCache = base64.StdEncoding.EncodeToString(data)
	}
}
