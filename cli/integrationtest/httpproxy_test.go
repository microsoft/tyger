// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build integrationtest

package integrationtest

import (
	"fmt"
	"os"
	"path"
	"testing"

	"github.com/microsoft/tyger/cli/internal/install"
	"github.com/stretchr/testify/require"
)

const composeFile = `
name: http-proxy-test

services:
  squid:
    image: ubuntu/squid

  tyger-proxy:
    image:  mcr.microsoft.com/devcontainers/base:ubuntu
    dns: 127.0.0.1 # DNS will not resolve anything outside of the docker network
    command: ["sleep", "infinity"]

  client:
    image:  mcr.microsoft.com/devcontainers/base:ubuntu
    dns: 127.0.0.1 # DNS will not resolve anything outside of the docker network
    command: ["sleep", "infinity"]

networks:
  default:
    driver: bridge
    ipam:
      driver: default
      config:
        - subnet: 192.168.250.0/24
`

func TestHttpProxy(t *testing.T) {
	t.Parallel()
	skipIfOnlyFastTests(t)
	skipIfUsingUnixSocket(t)

	s := NewComposeSession(t)
	defer s.Cleanup()

	s.CommandSucceeds("create")
	s.CommandSucceeds("start", "squid")

	bufferId := runTygerSucceeds(t, "buffer", "create")
	NewTygerCmdBuilder("buffer", "write", bufferId).Stdin("Hello").RunSucceeds(t)

	config := getCloudConfig(t)
	tygerUrl := fmt.Sprintf("https://%s", getLamnaOrgConfig(config).Api.DomainName)
	devConfig := getDevConfig(t)
	testAppUrl := devConfig["testAppUrl"].(string)
	certVersion := devConfig["pemCertSecret"].(map[string]any)["version"].(string)
	homeDir, err := os.UserHomeDir()
	require.NoError(t, err)
	certFilePath := path.Join(homeDir, fmt.Sprintf("tyger_test_client_cert_%s.pem", certVersion))

	credsFilePath := path.Join(t.TempDir(), "creds.yml")
	credsFileContent := fmt.Sprintf(`
serverUrl: %s
servicePrincipal: %s
certificatePath: /client_cert.pem
logPath: /logs
`, tygerUrl, testAppUrl)

	require.NoError(t, os.WriteFile(credsFilePath, []byte(credsFileContent), 0644))

	tygerPath := runCommandSucceeds(t, "which", "tyger")
	tygerProxyPath := runCommandSucceeds(t, "which", "tyger-proxy")
	s.CommandSucceeds("cp", tygerPath, "tyger-proxy:/usr/local/bin/tyger")
	s.CommandSucceeds("cp", tygerPath, "client:/usr/local/bin/tyger")
	s.CommandSucceeds("cp", tygerProxyPath, "tyger-proxy:/usr/local/bin/tyger-proxy")
	s.CommandSucceeds("cp", credsFilePath, "tyger-proxy:/creds.yml")
	s.CommandSucceeds("cp", certFilePath, "tyger-proxy:/client_cert.pem")

	s.CommandSucceeds("start", "tyger-proxy")

	squidProxy := "http://squid:3128"

	// Test tyger using the Squid proxy
	stdOut, stdErr, err := s.ShellExec("tyger-proxy", fmt.Sprintf("curl --fail %s/metadata -v", tygerUrl))
	t.Log("stdout", stdOut)
	t.Log("stdErr", stdErr)
	require.Error(t, err, "curl should fail because the proxy is not used")
	s.ShellExecSucceeds("tyger-proxy", fmt.Sprintf("curl --retry 5 --proxy %s --fail %s/metadata", squidProxy, tygerUrl))

	// Specify the proxy via environment variable
	s.ShellExecSucceeds("tyger-proxy", fmt.Sprintf("export HTTPS_PROXY=%s && tyger login -f /creds.yml --log-level trace && tyger buffer read %s > /dev/null", squidProxy, bufferId))

	// Specify the proxy in the config file
	s.ShellExecSucceeds("tyger-proxy", fmt.Sprintf("echo 'proxy: %s' >> /creds.yml", squidProxy))

	_, stdErr, err = s.ShellExec("tyger-proxy", "tyger login -f /creds.yml --log-level trace --log-format json")
	require.NoError(t, err, "tyger login should succeed", stdErr)

	parsedLogLines, err := install.ParseJsonLogs([]byte(stdErr))
	require.NoError(t, err, "failed to parse log lines")
	foundCount := 0
	for _, line := range parsedLogLines {
		if line["message"] == "Sending request" {
			foundCount++
			proxyLogEntry := line["proxy"]
			require.Equal(t, squidProxy, proxyLogEntry, "proxy argument missing from log entry")
		}
	}
	require.Greater(t, foundCount, 0, "no log entries found with proxy argument")

	s.ShellExecSucceeds("tyger-proxy", fmt.Sprintf("tyger buffer read %s > /dev/null", bufferId))

	// Now start up the tyger proxy
	s.ShellExecSucceeds("tyger-proxy", "tyger-proxy start -f /creds.yml")

	// Connect to it from the client container
	s.CommandSucceeds("start", "client")
	s.ShellExecSucceeds("client", fmt.Sprintf("tyger login http://tyger-proxy:6888 && tyger buffer read %s > /dev/null", bufferId))

	// Now repeat without TLS certificate validation

	certsDir := "/etc/ssl/certs"

	// Remove root CA certificates from containers
	s.ShellExecSucceeds("tyger-proxy", fmt.Sprintf("rm -rf %s", certsDir))
	s.ShellExecSucceeds("client", fmt.Sprintf("rm -rf %s", certsDir))

	_, _, err = s.ShellExec("tyger-proxy", fmt.Sprintf("curl --proxy %s --fail %s/metadata", squidProxy, tygerUrl))
	require.Error(t, err, "curl should fail because the root CA certificates have been removed")
	s.ShellExecSucceeds("tyger-proxy", fmt.Sprintf("curl --retry 5 --proxy %s --insecure --fail %s/metadata", squidProxy, tygerUrl))

	// disable TLS certificate validation in the config file
	s.ShellExecSucceeds("tyger-proxy", fmt.Sprintf("echo 'disableTlsCertificateValidation: true' >> /creds.yml"))
	s.ShellExecSucceeds("tyger-proxy", fmt.Sprintf("tyger login -f /creds.yml && tyger buffer read %s > /dev/null", bufferId))

	// Now restart tyger-proxy with TLS certificate validation disabled
	s.ShellExecSucceeds("tyger-proxy", "pgrep tyger-proxy | xargs kill && tyger-proxy start -f /creds.yml")

	// And connect to it from the client with TLS certificate validation also disabled
	s.ShellExecSucceeds("client", fmt.Sprintf("tyger login http://tyger-proxy:6888 --disable-tls-certificate-validation && tyger buffer read %s > /dev/null", bufferId))
}

type ComposeSession struct {
	t   *testing.T
	dir string
}

func NewComposeSession(t *testing.T) *ComposeSession {
	s := &ComposeSession{t: t, dir: t.TempDir()}
	require.NoError(t, os.WriteFile(path.Join(s.dir, "/docker-compose.yml"), []byte(composeFile), 0644))
	s.Cleanup()
	return s
}

func (s *ComposeSession) CommandSucceeds(args ...string) string {
	s.t.Helper()
	b := NewCmdBuilder("docker", append([]string{"compose"}, args...)...)
	b.Dir(s.dir)
	return b.RunSucceeds(s.t)
}

func (s *ComposeSession) Command(args ...string) (stdout string, stderr string, err error) {
	b := NewCmdBuilder("docker", append([]string{"compose"}, args...)...)
	b.Dir(s.dir)
	return b.Run()
}

func (s *ComposeSession) ShellExecSucceeds(service string, command string) string {
	s.t.Helper()
	return s.CommandSucceeds("exec", "-T", service, "bash", "-c", command)
}

func (s *ComposeSession) ShellExec(service string, command string) (stdout string, stderr string, err error) {
	return s.Command("exec", "-T", service, "bash", "-c", command)
}

func (s *ComposeSession) Cleanup() {
	s.CommandSucceeds("kill")
	s.CommandSucceeds("down")
	s.CommandSucceeds("rm")
}
