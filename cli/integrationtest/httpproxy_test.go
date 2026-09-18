// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build integrationtest

package integrationtest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/microsoft/tyger/cli/internal/controlplane"
	"github.com/microsoft/tyger/cli/internal/controlplane/model"
	"github.com/microsoft/tyger/cli/internal/install"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

// startAzureCliBridge lets the test container use the test runner's Azure CLI login
// without installing az or copying its credential cache. The container's az shell
// shim POSTs JSON arguments over a Unix socket; the test process runs its local az
// and returns base64-encoded stdout/stderr and the exit code. Authentication runs
// outside Squid because this test checks Tyger's proxy handling, not Azure CLI's.
//
// The private socket/shim directory lives in the shared workspace rather than /tmp
// so TYGER_DOCKER_HOST_PATH_TRANSLATIONS can map it to a Docker-host bind-mount path
// when the test runner itself is in a devcontainer.
func startAzureCliBridge(t *testing.T, handler http.Handler) string {
	t.Helper()

	socketDir, err := os.MkdirTemp(".", ".az-bridge-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(socketDir)) })
	socketDir, err = filepath.Abs(socketDir)
	require.NoError(t, err)

	const shim = `#!/usr/bin/env bash
set -euo pipefail

response=$(jq -cn --args '$ARGS.positional' -- "$@" |
    curl --silent --show-error --fail --max-time 60 --noproxy '*' \
        --unix-socket /az-bridge/az.sock \
        --header 'Content-Type: application/json' \
        --data-binary @- http://localhost/)

jq -r '.stdout // ""' <<<"$response" | base64 --decode
jq -r '.stderr // ""' <<<"$response" | base64 --decode >&2
exit "$(jq -r '.exitCode' <<<"$response")"
`
	require.NoError(t, os.WriteFile(filepath.Join(socketDir, "az"), []byte(shim), 0700))

	socketPath := filepath.Join(socketDir, "az.sock")
	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	t.Cleanup(func() { listener.Close() })
	server := &http.Server{Handler: handler, ReadTimeout: 5 * time.Second, WriteTimeout: time.Minute}
	t.Cleanup(func() { require.NoError(t, server.Close()) })
	go server.Serve(listener)

	return translateDockerHostPath(t, socketDir)
}

func azureCliBridgeHandler(azPath string) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		var args []string
		if request.Method != http.MethodPost || request.URL.Path != "/" ||
			json.NewDecoder(http.MaxBytesReader(response, request.Body, 16*1024)).Decode(&args) != nil {
			http.Error(response, "invalid Azure CLI bridge request", http.StatusBadRequest)
			return
		}

		ctx, cancel := context.WithTimeout(request.Context(), 30*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, azPath, args...)
		command.WaitDelay = time.Second
		var stdout, stderr bytes.Buffer
		command.Stdout = &stdout
		command.Stderr = &stderr
		exitCode := 0
		if err := command.Run(); err != nil {
			exitCode = 1
			var exitError *exec.ExitError
			if errors.As(err, &exitError) && exitError.ExitCode() >= 0 {
				exitCode = exitError.ExitCode()
			} else {
				fmt.Fprintf(&stderr, "Azure CLI bridge: %v\n", err)
			}
		}
		response.Header().Set("Content-Type", "application/json")
		json.NewEncoder(response).Encode(struct {
			Stdout   []byte `json:"stdout"`
			Stderr   []byte `json:"stderr"`
			ExitCode int    `json:"exitCode"`
		}{stdout.Bytes(), stderr.Bytes(), exitCode})
	})
}

func translateDockerHostPath(t *testing.T, containerPath string) string {
	t.Helper()

	hostPath := containerPath
	longestMatch := 0
	for mapping := range strings.SplitSeq(os.Getenv("TYGER_DOCKER_HOST_PATH_TRANSLATIONS"), ":") {
		if mapping == "" {
			continue
		}
		source, destination, ok := strings.Cut(mapping, "=")
		require.True(t, ok && source != "" && destination != "", "invalid Docker host path translation")
		source = filepath.Clean(source)
		relativePath, err := filepath.Rel(source, containerPath)
		require.NoError(t, err)
		if relativePath != ".." && !strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) && len(source) > longestMatch {
			hostPath = filepath.Join(destination, relativePath)
			longestMatch = len(source)
		}
	}
	return hostPath
}

func TestHttpProxy(t *testing.T) {
	t.Parallel()
	skipIfOnlyFastTests(t)
	skipIfUsingUnixSocket(t)

	proxyConfigString := runCommandSucceeds(t, "make", "-s", "-C", "../..", "get-login-spec")
	proxyConfig := controlplane.LoginConfig{}
	require.NoError(t, yaml.Unmarshal([]byte(proxyConfigString), &proxyConfig))

	proxyVolumes := []map[string]any{}
	if proxyConfig.AzureCli {
		azPath, err := exec.LookPath("az")
		require.NoError(t, err)
		hostSocketDir := startAzureCliBridge(t, azureCliBridgeHandler(azPath))
		proxyVolumes = append(proxyVolumes, map[string]any{
			"type": "bind", "source": hostSocketDir, "target": "/az-bridge", "read_only": true,
		})
	}
	proxyVolumesJson, err := json.Marshal(proxyVolumes)
	require.NoError(t, err)

	composeFile := fmt.Sprintf(`
name: http-proxy-test

services:
  squid:
    image: ubuntu/squid
    healthcheck:
      test: ["CMD", "bash", "-c", "</dev/tcp/127.0.0.1/3128"]
      interval: 1s
      timeout: 1s
      retries: 30

  tyger-proxy:
    image: mcr.microsoft.com/devcontainers/base:ubuntu
    volumes: %s
    dns: 127.0.0.1 # DNS will not resolve anything outside of the docker network
    command: ["sleep", "infinity"]
    environment:
      - ACTIONS_ID_TOKEN_REQUEST_URL=${ACTIONS_ID_TOKEN_REQUEST_URL}
      - ACTIONS_ID_TOKEN_REQUEST_TOKEN=${ACTIONS_ID_TOKEN_REQUEST_TOKEN}

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
  `, proxyVolumesJson)

	s := NewComposeSession(t, composeFile)
	defer s.Cleanup()

	s.CommandSucceeds("create")
	s.CommandSucceeds("start", "squid")
	s.WaitForHealthy("squid")

	bufferId := runTygerSucceeds(t, "buffer", "create")
	NewTygerCmdBuilder("buffer", "write", bufferId).Stdin("Hello").RunSucceeds(t)

	config := getCloudConfig(t)
	tygerUrl := fmt.Sprintf("https://%s", getLamnaOrgConfig(config).Api.DomainName)

	sourceCertPath := proxyConfig.CertificatePath
	if sourceCertPath != "" {
		proxyConfig.CertificatePath = "/client_cert.pem"
	}
	proxyConfig.LogPath = "/logs"

	b, err := yaml.Marshal(proxyConfig)
	require.NoError(t, err)
	proxyConfigString = string(b)
	proxyConfigFilePath := path.Join(t.TempDir(), "proxy-config.yml")
	require.NoError(t, os.WriteFile(proxyConfigFilePath, []byte(proxyConfigString), 0644))

	tygerPath := runCommandSucceeds(t, "which", "tyger")
	tygerProxyPath := runCommandSucceeds(t, "which", "tyger-proxy")
	s.CommandSucceeds("cp", tygerPath, "tyger-proxy:/usr/local/bin/tyger")
	s.CommandSucceeds("cp", tygerPath, "client:/usr/local/bin/tyger")
	s.CommandSucceeds("cp", tygerProxyPath, "tyger-proxy:/usr/local/bin/tyger-proxy")
	s.CommandSucceeds("cp", proxyConfigFilePath, "tyger-proxy:/creds.yml")
	if sourceCertPath != "" {
		s.CommandSucceeds("cp", sourceCertPath, "tyger-proxy:/client_cert.pem")
	}

	s.CommandSucceeds("start", "tyger-proxy")
	if proxyConfig.AzureCli {
		s.ShellExecSucceeds("tyger-proxy", "ln -s /az-bridge/az /usr/local/bin/az")
	}

	squidProxy := "http://squid:3128"

	// Test tyger using the Squid proxy
	stdOut, stdErr, err := s.ShellExec("tyger-proxy", fmt.Sprintf("curl --fail %s/metadata -v", tygerUrl))
	if err == nil {
		t.Log("stdout", stdOut)
		t.Log("stdErr", stdErr)
	}
	require.Error(t, err, "curl should fail because the proxy is not used")
	s.ShellExecSucceeds("tyger-proxy", fmt.Sprintf("curl --retry 5 --retry-connrefused --retry-delay 1 --proxy %s --fail %s/metadata", squidProxy, tygerUrl))

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

	defer func() {
		time.Sleep(time.Second)
		out, _, _ := s.ShellExec("tyger-proxy", `for f in $(ls -1 /logs | sort); do echo "===== $f ====="; cat "/logs/$f"; echo; done`)
		t.Log(out)
	}()

	// Connect to it from the client container
	s.CommandSucceeds("start", "client")
	s.ShellExecSucceeds("client", fmt.Sprintf("curl --fail --retry 5 --retry-connrefused --retry-delay 1 http://tyger-proxy:6888/metadata > /dev/stderr && tyger login http://tyger-proxy:6888 && tyger buffer read --log-level trace %s > /dev/null", bufferId))

	// Now repeat without TLS certificate validation

	certsDir := "/etc/ssl/certs"

	// Remove root CA certificates from containers
	s.ShellExecSucceeds("tyger-proxy", fmt.Sprintf("rm -rf %s", certsDir))
	s.ShellExecSucceeds("client", fmt.Sprintf("rm -rf %s", certsDir))

	_, _, err = s.ShellExec("tyger-proxy", fmt.Sprintf("curl --proxy %s --fail %s/metadata", squidProxy, tygerUrl))
	require.Error(t, err, "curl should fail because the root CA certificates have been removed")
	s.ShellExecSucceeds("tyger-proxy", fmt.Sprintf("curl --retry 5 --retry-connrefused --retry-delay 1 --proxy %s --insecure --fail %s/metadata", squidProxy, tygerUrl))

	// Use CA certificates that are embedded in the tyger binary
	s.ShellExecSucceeds("tyger-proxy", "echo 'tlsCaCertificates: embedded' >> /creds.yml")
	s.ShellExecSucceeds("tyger-proxy", fmt.Sprintf("tyger login -f /creds.yml && tyger buffer read %s > /dev/null", bufferId))

	// Now start up the tyger proxy using embedded CA certificates
	s.ShellExecSucceeds("tyger-proxy", "pgrep tyger-proxy | xargs kill && tyger-proxy start -f /creds.yml")

	// Then connect to it from the client, which should use the CA certificates that the proxy publishes in its metadata
	s.ShellExecSucceeds("client", fmt.Sprintf("curl --fail --retry 5 --retry-connrefused --retry-delay 1 http://tyger-proxy:6888/metadata && tyger login http://tyger-proxy:6888 && tyger buffer read %s > /dev/null", bufferId))
}

func TestTygerProxyOverSsh(t *testing.T) {
	// Deliberately not parallel because the interactive portion of Login() does not perform retries and
	// when running in GitHub actions, sshd may refuse connections if too many are opened simultaneously.

	skipIfOnlyFastTests(t)
	skipIfNotUsingSSH(t)

	const composeFile = `
name: tyger-proxy-ssh-test

services:
  tyger-proxy:
    image:  mcr.microsoft.com/devcontainers/base:ubuntu
    command: ["tyger-proxy", "run", "-f", "/proxy-config.yml"]
    network_mode: bridge

  client:
    image:  mcr.microsoft.com/devcontainers/base:ubuntu
    command: ["sleep", "infinity"]
    network_mode: bridge

`

	s := NewComposeSession(t, composeFile)
	defer s.Cleanup()

	s.CommandSucceeds("create")

	ssh_host := runCommandSucceeds(t, "docker", "inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", "tyger-test-ssh")

	c, _ := controlplane.GetClientFromCache()

	tempDir := t.TempDir()

	const containerKeyFile = "/root/id"

	sshUrl := *c.RawControlPlaneUrl
	sshUrl.Host = ssh_host
	sshUrlQuery := sshUrl.Query()
	sshUrlQuery.Set("option[StrictHostKeyChecking]", "no")
	sshUrlQuery.Set("option[IdentityFile]", containerKeyFile)

	sshUrl.RawQuery = sshUrlQuery.Encode()

	proxyConfig := controlplane.LoginConfig{
		ServerUrl: sshUrl.String(),
		LogPath:   "/logs",
	}

	b, err := yaml.Marshal(proxyConfig)
	require.NoError(t, err)
	proxyConfigString := string(b)
	proxyConfigFilePath := path.Join(tempDir, "proxy-config.yml")
	require.NoError(t, os.WriteFile(proxyConfigFilePath, []byte(proxyConfigString), 0644))

	tygerPath := runCommandSucceeds(t, "which", "tyger")
	tygerProxyPath := runCommandSucceeds(t, "which", "tyger-proxy")
	s.CommandSucceeds("cp", tygerPath, "tyger-proxy:/usr/local/bin/tyger")
	s.CommandSucceeds("cp", tygerPath, "client:/usr/local/bin/tyger")
	s.CommandSucceeds("cp", tygerProxyPath, "tyger-proxy:/usr/local/bin/tyger-proxy")
	s.CommandSucceeds("cp", proxyConfigFilePath, "tyger-proxy:/proxy-config.yml")

	localKeyFile := path.Join(tempDir, "id")
	runCommandSucceeds(t, "ssh-keygen", "-t", "ed25519", "-f", localKeyFile, "-N", "")
	s.CommandSucceeds("cp", localKeyFile, "tyger-proxy:/root/id")

	runCommandSucceeds(t, "ssh-copy-id", "-f", "-i", localKeyFile+".pub", "-o", "StrictHostKeyChecking=no", "tygersshhost")

	s.CommandSucceeds("start", "tyger-proxy")
	defer func() {
		logs := s.CommandSucceeds("logs", "tyger-proxy")
		t.Log(logs)
	}()

	s.CommandSucceeds("start", "client")

	runSpec := fmt.Sprintf(`
job:
  codespec:
    image: %s
    buffers:
      inputs: ["input"]
      outputs: ["output"]
    command:
      - "sh"
      - "-c"
      - |
        set -euo pipefail
        inp=$(cat "$INPUT_PIPE")
        echo "${inp}: Bonjour" > "$OUTPUT_PIPE"
timeoutSeconds: 600`, BasicImage)

	runSpecPath := filepath.Join(tempDir, "runspec.yaml")
	require.NoError(t, os.WriteFile(runSpecPath, []byte(runSpec), 0644))

	runId := runTygerSucceeds(t, "run", "create", "-f", runSpecPath)

	tygerProxyContainerId := s.CommandSucceeds("ps", "-q", "tyger-proxy")

	proxyIp := runCommandSucceeds(t, "docker", "inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", tygerProxyContainerId)

	// it could take some time for the proxy to be ready, so retry a few times
	for attempt := 1; ; attempt++ {
		_, stdErr, err := s.ShellExec("client", fmt.Sprintf("tyger login http://%s:6888", proxyIp))
		if err == nil {
			break
		}
		if attempt >= 5 {
			t.Fatalf("tyger login failed after %d attempts: %v\n%s", attempt, err, stdErr)
		}
		time.Sleep(time.Duration(attempt*2) * time.Second)
	}

	run := model.Run{}
	runYaml := s.ShellExecSucceeds("client", "tyger run show "+runId)
	require.NoError(t, yaml.Unmarshal([]byte(runYaml), &run))

	inputBufferId := run.Job.Buffers["input"]
	outputBufferId := run.Job.Buffers["output"]

	s.ShellExecSucceeds("client", "echo Carl | tyger buffer write "+inputBufferId)
	output := s.ShellExecSucceeds("client", "tyger buffer read "+outputBufferId)
	require.Equal(t, "Carl: Bonjour", output)

	// repeat with ephemeral buffers

	runId = runTygerSucceeds(t, "run", "create", "-f", runSpecPath, "-b", "input=_", "-b", "output=_")
	runYaml = s.ShellExecSucceeds("client", "tyger run show "+runId)
	require.NoError(t, yaml.Unmarshal([]byte(runYaml), &run))

	inputBufferId = run.Job.Buffers["input"]
	outputBufferId = run.Job.Buffers["output"]

	_, stderr, err := s.ShellExec("client", fmt.Sprintf("echo Isabelle | tyger buffer write %s --log-level trace", inputBufferId))
	require.NoError(t, err, stderr)
	t.Log(stderr)
	output, stderr, err = s.ShellExec("client", fmt.Sprintf("tyger buffer read %s --log-level trace", outputBufferId))
	require.NoError(t, err, stderr)
	t.Log(stderr)
	require.Equal(t, "Isabelle: Bonjour", output)
}

type ComposeSession struct {
	t   *testing.T
	dir string
}

func NewComposeSession(t *testing.T, composeFileContent string) *ComposeSession {
	s := &ComposeSession{t: t, dir: t.TempDir()}
	require.NoError(t, os.WriteFile(path.Join(s.dir, "/docker-compose.yml"), []byte(composeFileContent), 0644))
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

func (s *ComposeSession) WaitForHealthy(service string) {
	s.t.Helper()

	containerId := s.CommandSucceeds("ps", "-q", service)
	var lastStatus string
	var lastStdErr string
	var lastErr error

	for attempt := 1; attempt <= 60; attempt++ {
		lastStatus, lastStdErr, lastErr = runCommand("docker", "inspect", "--format", "{{.State.Health.Status}}", containerId)
		if lastErr == nil && lastStatus == "healthy" {
			return
		}

		if attempt < 60 {
			time.Sleep(time.Second)
		}
	}

	logs, _, _ := s.Command("logs", service)
	if logs != "" {
		s.t.Log(logs)
	}
	s.t.Fatalf("timed out waiting for %s to become healthy: status=%q error=%v\n%s", service, lastStatus, lastErr, lastStdErr)
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
