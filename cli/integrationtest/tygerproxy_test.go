// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build integrationtest

package integrationtest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sync"
	"testing"

	"github.com/microsoft/tyger/cli/internal/controlplane"
	"github.com/microsoft/tyger/cli/internal/controlplane/model"
	"github.com/microsoft/tyger/cli/internal/tygerproxy"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestProxiedRequests(t *testing.T) {
	t.Parallel()
	skipIfUsingUnixSocket(t)

	require := require.New(t)

	// create a run
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
        echo "this is a log message"
timeoutSeconds: 600`, BasicImage)

	tempDir := t.TempDir()
	runSpecPath := filepath.Join(tempDir, "runspec.yaml")
	require.NoError(os.WriteFile(runSpecPath, []byte(runSpec), 0644))

	runId := runTygerSucceeds(t, "run", "create", "--file", runSpecPath)

	tygerClient, err := controlplane.GetClientFromCache()

	proxyOptions := tygerproxy.ProxyOptions{}

	proxyLogBuffer := SyncBuffer{}
	logger := zerolog.New(&proxyLogBuffer)

	closeProxy, err := tygerproxy.RunProxy(context.Background(), tygerClient, &proxyOptions, logger)
	require.NoError(err)
	defer closeProxy()

	cachePath := path.Join(tempDir, "cache")

	NewTygerCmdBuilder("login", fmt.Sprintf("http://localhost:%d", proxyOptions.Port)).
		Env(controlplane.CacheFileEnvVarName, cachePath).
		RunSucceeds(t)

	runJson := NewTygerCmdBuilder("run", "show", runId).
		Env(controlplane.CacheFileEnvVarName, cachePath).
		RunSucceeds(t)

	var run model.Run
	require.NoError(json.Unmarshal([]byte(runJson), &run))

	inputBufferId := run.Job.Buffers["input"]
	outputBufferId := run.Job.Buffers["output"]

	NewTygerCmdBuilder("buffer", "write", inputBufferId).
		Env(controlplane.CacheFileEnvVarName, cachePath).
		Stdin("Hello").
		RunSucceeds(t)

	waitForRunSuccess(t, runId)

	output := NewTygerCmdBuilder("buffer", "read", outputBufferId).
		Env(controlplane.CacheFileEnvVarName, cachePath).
		RunSucceeds(t)

	require.Equal("Hello: Bonjour", output)

	logs := NewTygerCmdBuilder("run", "logs", runId).
		Env(controlplane.CacheFileEnvVarName, cachePath).
		RunSucceeds(t)

	require.Contains(logs, "this is a log message")

	// now look through the proxy logs to make sure the requests
	// actually went through the proxy
	scanner := bufio.NewScanner(&proxyLogBuffer)

	var parsedLines []map[string]any

	for scanner.Scan() {
		line := scanner.Bytes()
		t.Log(string(line))
		var obj map[string]interface{}
		require.NoError(json.Unmarshal(line, &obj))
		parsedLines = append(parsedLines, obj)
	}

	require.NoError(scanner.Err())
	findEntry := func(pairs ...string) any {
		for _, l := range parsedLines {
			found := true
			for i := 0; i < len(pairs); i += 2 {
				key := pairs[i]
				expectedValue := pairs[i+1]
				if value, ok := l[key]; !ok || value != expectedValue {
					found = false
				}
			}
			if found {
				return l
			}
		}
		return nil
	}

	apiVersion := fmt.Sprintf("%s=%s", controlplane.ApiVersionQueryParam, controlplane.DefaultApiVersion)

	require.NotNil(
		findEntry(
			"method", "GET",
			"url", fmt.Sprintf("/runs/%s?%s", runId, apiVersion)),
		"Could not find run show in logs")

	require.NotNil(
		findEntry(
			"method", "POST",
			"url", fmt.Sprintf("/buffers/%s/access?%s&writeable=true", inputBufferId, apiVersion)),
		"Could not find input buffer access in logs")

	require.NotNil(
		findEntry(
			"method", "POST",
			"url", fmt.Sprintf("/buffers/%s/access?%s&writeable=false", outputBufferId, apiVersion)),
		"Could not find output buffer access in logs")

	require.NotNil(
		findEntry(
			"method", "CONNECT"),
		"Could not find data plane tunneling in logs")

	require.NotNil(
		findEntry(
			"method", "GET",
			"url", fmt.Sprintf("/runs/%s/logs?%s", runId, apiVersion)),
		"Could not find run logs request in logs")
}

func TestProxiedRequestsFromAllowedCIDR(t *testing.T) {
	t.Parallel()
	skipIfUsingUnixSocket(t)

	require := require.New(t)

	proxyOptions := tygerproxy.ProxyOptions{
		LoginConfig: controlplane.LoginConfig{
			AllowedClientCIDRs: []string{"127.0.0.1/32"},
		},
	}

	proxyLogBuffer := SyncBuffer{}
	logger := zerolog.New(&proxyLogBuffer)

	tygerClient, err := controlplane.GetClientFromCache()
	require.NoError(err)
	closeProxy, err := tygerproxy.RunProxy(context.Background(), tygerClient, &proxyOptions, logger)
	defer closeProxy()
	resp, err := tygerClient.ControlPlaneClient.Get(fmt.Sprintf("http://localhost:%d/metadata", proxyOptions.Port))
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
}

func TestProxiedRequestsFromDisallowedAllowedCIDR(t *testing.T) {
	t.Parallel()
	skipIfUsingUnixSocket(t)

	require := require.New(t)

	proxyOptions := tygerproxy.ProxyOptions{
		LoginConfig: controlplane.LoginConfig{
			AllowedClientCIDRs: []string{"8.0.0.1/32"},
		},
	}

	tygerClient, err := controlplane.GetClientFromCache()
	require.NoError(err)
	proxyLogBuffer := SyncBuffer{}
	logger := zerolog.New(&proxyLogBuffer)

	closeProxy, err := tygerproxy.RunProxy(context.Background(), tygerClient, &proxyOptions, logger)
	defer closeProxy()
	resp, err := tygerClient.ControlPlaneClient.Get(fmt.Sprintf("http://localhost:%d/runs/1", proxyOptions.Port))
	require.NoError(err)
	require.Equal(http.StatusForbidden, resp.StatusCode)

	// The metadata endpoint should still be accessible from the loopback address
	resp, err = tygerClient.ControlPlaneClient.Get(fmt.Sprintf("http://localhost:%d/metadata", proxyOptions.Port))
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
}

func TestRunningProxyOnSamePort(t *testing.T) {
	t.Parallel()
	skipIfUsingUnixSocket(t)

	require := require.New(t)
	tygerClient, err := controlplane.GetClientFromCache()
	require.NoError(err)

	proxyOptions := tygerproxy.ProxyOptions{
		LoginConfig: controlplane.LoginConfig{
			ServerUrl: tygerClient.ControlPlaneUrl.String(),
		},
	}
	proxyLogBuffer := SyncBuffer{}
	logger := zerolog.New(&proxyLogBuffer)

	closeProxy, err := tygerproxy.RunProxy(context.Background(), tygerClient, &proxyOptions, logger)
	require.NoError(err)
	defer closeProxy()

	_, err = tygerproxy.RunProxy(context.Background(), tygerClient, &proxyOptions, logger)
	require.ErrorIs(err, tygerproxy.ErrProxyAlreadyRunning)
}

func TestRunningProxyOnSamePortDifferentTarget(t *testing.T) {
	t.Parallel()
	skipIfUsingUnixSocket(t)

	require := require.New(t)
	tygerClient, err := controlplane.GetClientFromCache()
	require.NoError(err)

	proxyOptions := tygerproxy.ProxyOptions{
		LoginConfig: controlplane.LoginConfig{
			ServerUrl: tygerClient.ControlPlaneUrl.String(),
		},
	}
	proxyLogBuffer := SyncBuffer{}
	logger := zerolog.New(&proxyLogBuffer)

	closeProxy, err := tygerproxy.RunProxy(context.Background(), tygerClient, &proxyOptions, logger)
	require.NoError(err)
	defer closeProxy()

	secondProxyOptions := *&proxyOptions
	secondProxyOptions.LoginConfig.ServerUrl = "http://someotherserver"

	_, err = tygerproxy.RunProxy(context.Background(), tygerClient, &secondProxyOptions, logger)
	require.ErrorIs(err, tygerproxy.ErrProxyAlreadyRunningWrongTarget)
}

// A goroutine-safe bytes.SyncBuffer
type SyncBuffer struct {
	buffer bytes.Buffer
	mutex  sync.Mutex
}

func (s *SyncBuffer) Write(p []byte) (n int, err error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.buffer.Write(p)
}

func (s *SyncBuffer) String() string {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.buffer.String()
}

func (s *SyncBuffer) Read(p []byte) (n int, err error) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.buffer.Read(p)
}
