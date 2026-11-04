// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package dockerinstall

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestInstaller(t *testing.T, handler http.HandlerFunc) *Installer {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_ping" {
			w.Header().Set("API-Version", "1.41")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		handler(w, r)
	}))
	t.Cleanup(server.Close)

	dockerClient, err := client.New(client.WithHost(server.URL))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, dockerClient.Close()) })

	return &Installer{
		Config: &DockerEnvironmentConfig{EnvironmentName: "test"},
		client: dockerClient,
	}
}

func TestCreateNetwork(t *testing.T) {
	for _, tc := range []struct {
		name           string
		subnet         string
		existing       bool
		existingSubnet string
		expectedError  string
	}{
		{name: "default"},
		{name: "existing default", existing: true},
		{name: "new subnet", subnet: "172.28.0.0/16"},
		{name: "matching subnet", subnet: "172.28.0.0/16", existing: true, existingSubnet: "172.28.0.0/16"},
		{name: "different subnet", subnet: "172.28.0.0/16", existing: true, existingSubnet: "172.29.0.0/16", expectedError: "different subnet"},
		{name: "missing IPAM config", subnet: "172.28.0.0/16", existing: true, expectedError: "different subnet"},
		{name: "invalid subnet", subnet: "invalid", expectedError: "error parsing network subnet"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			created := false
			inst := newTestInstaller(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.Method + " " + r.URL.Path {
				case "GET /v1.41/networks/tyger-test-network":
					if !tc.existing {
						w.WriteHeader(http.StatusNotFound)
						io.WriteString(w, `{"message":"network not found"}`)
						return
					}
					config := []map[string]string{}
					if tc.existingSubnet != "" {
						config = append(config, map[string]string{"Subnet": tc.existingSubnet})
					}
					assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"IPAM": map[string]any{"Config": config}}))
				case "POST /v1.41/networks/create":
					created = true
					var request network.CreateRequest
					if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
						return
					}
					assert.Equal(t, "tyger-test-network", request.Name)
					assert.Equal(t, "bridge", request.Driver)
					if tc.subnet == "" {
						assert.Nil(t, request.IPAM)
					} else if assert.NotNil(t, request.IPAM) && assert.Len(t, request.IPAM.Config, 1) {
						assert.Equal(t, tc.subnet, request.IPAM.Config[0].Subnet.String())
					}
					io.WriteString(w, `{"Id":"network-id"}`)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
					w.WriteHeader(http.StatusInternalServerError)
				}
			})
			if tc.subnet != "" {
				inst.Config.Network = &NetworkConfig{Subnet: tc.subnet}
			}
			err := inst.createNetwork(context.Background())
			if tc.expectedError != "" {
				require.ErrorContains(t, err, tc.expectedError)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, !tc.existing && tc.expectedError == "", created)
		})
	}
}

func TestCreateDataPlaneContainer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Docker installation requires WSL on Windows")
	}

	created, copied, started := false, false, false
	inst := newTestInstaller(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v1.41/volumes/tyger-test-buffers":
			io.WriteString(w, `{"Name":"tyger-test-buffers"}`)
		case "GET /v1.41/containers/tyger-test-data-plane/json":
			if !created {
				w.WriteHeader(http.StatusNotFound)
				io.WriteString(w, `{"message":"container not found"}`)
				return
			}
			io.WriteString(w, `{"State":{"Running":true,"Health":{"Status":"healthy"}}}`)
		case "GET /v1.41/images/data-plane/json":
			io.WriteString(w, `{}`)
		case "POST /v1.41/containers/create":
			created = true
			assert.Equal(t, "tyger-test-data-plane", r.URL.Query().Get("name"))
			var request container.CreateRequest
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
				return
			}
			assert.Equal(t, "data-plane", request.Image)
			assert.NotEmpty(t, request.Labels[containerSpecHashLabel])
			port := network.MustParsePort("8080/tcp")
			assert.Contains(t, request.ExposedPorts, port)
			if assert.Len(t, request.HostConfig.PortBindings[port], 1) {
				binding := request.HostConfig.PortBindings[port][0]
				assert.Equal(t, "127.0.0.1", binding.HostIP.String())
				assert.Equal(t, "12345", binding.HostPort)
			}
			if assert.NotNil(t, request.Healthcheck) {
				assert.Zero(t, request.Healthcheck.StartPeriod)
				assert.Zero(t, request.Healthcheck.StartInterval)
			}
			io.WriteString(w, `{"Id":"container-id"}`)
		case "PUT /v1.41/containers/tyger-test-data-plane/archive":
			copied = true
			assert.Equal(t, "/app", r.URL.Query().Get("path"))
			assert.Equal(t, "true", r.URL.Query().Get("noOverwriteDirNonDir"))
			reader := tar.NewReader(r.Body)
			header, err := reader.Next()
			if !assert.NoError(t, err) {
				return
			}
			assert.True(t, strings.HasPrefix(header.Name, "tyger-data-plane-public-primary-"))
			content, err := io.ReadAll(reader)
			assert.NoError(t, err)
			assert.Equal(t, "public key", string(content))
		case "POST /v1.41/containers/container-id/start":
			started = true
			assert.True(t, copied)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	inst.Config.InstallationPath = t.TempDir()
	inst.Config.UserId = strconv.Itoa(os.Getuid())
	inst.Config.AllowedGroupId = strconv.Itoa(os.Getgid())
	inst.Config.DataPlaneImage = "data-plane"
	inst.Config.DataPlanePort = 12345
	publicKeyPath := filepath.Join(t.TempDir(), "public.pem")
	require.NoError(t, os.WriteFile(publicKeyPath, []byte("public key"), 0600))
	inst.Config.SigningKeys.Primary = &KeyPair{PublicKey: publicKeyPath}

	require.NoError(t, inst.createDataPlaneContainer(context.Background()))
	assert.True(t, created)
	assert.True(t, copied)
	assert.True(t, started)
}

func TestPullImage(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(strconv.FormatBool(fail), func(t *testing.T) {
			inst := newTestInstaller(t, func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "POST", r.Method)
				assert.Equal(t, "/v1.41/images/create", r.URL.Path)
				assert.Equal(t, "docker.io/example/image", r.URL.Query().Get("fromImage"))
				if fail {
					io.WriteString(w, `{"errorDetail":{"message":"pull denied"},"error":"pull denied"}`)
				} else {
					io.WriteString(w, `{"status":"download complete"}`)
				}
			})
			err := inst.pullImage(context.Background(), "example/image:latest", true)
			if fail {
				require.ErrorContains(t, err, "pull denied")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestContainerWaitAndLogs(t *testing.T) {
	inst := newTestInstaller(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "POST /v1.41/containers/test/wait":
			assert.Equal(t, "not-running", r.URL.Query().Get("condition"))
			io.WriteString(w, `{"StatusCode":42}`)
		case "GET /v1.41/containers/test/logs":
			assert.Equal(t, "1", r.URL.Query().Get("stdout"))
			assert.Equal(t, "1", r.URL.Query().Get("stderr"))
			assert.Equal(t, "5", r.URL.Query().Get("tail"))
			for i, text := range []string{"standard output", "standard error"} {
				frame := make([]byte, 8)
				frame[0] = byte(i + 1)
				binary.BigEndian.PutUint32(frame[4:], uint32(len(text)))
				_, err := w.Write(append(frame, text...))
				assert.NoError(t, err)
			}
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	exitCode, err := inst.waitForContainerToComplete(context.Background(), "test")
	require.NoError(t, err)
	assert.Equal(t, 42, exitCode)

	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	require.NoError(t, inst.getContainerLogs(context.Background(), "test", false, 5, stdout, stderr))
	assert.Equal(t, "standard output", stdout.String())
	assert.Equal(t, "standard error", stderr.String())
}
