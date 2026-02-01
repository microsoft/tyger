// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build integrationtest

package integrationtest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os/exec"
	"slices"
	"testing"
	"time"

	"github.com/microsoft/tyger/cli/internal/client"
	"github.com/microsoft/tyger/cli/internal/controlplane"
	"github.com/microsoft/tyger/cli/internal/controlplane/model"
	"github.com/microsoft/tyger/cli/internal/install/cloudinstall"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

var (
	runOnlyFastTestsFlag = flag.Bool("fast", false, "only run \"fast\" tests")
	assertRoleFlag       = flag.String("assert-role", "", "assert that the user has this role before running the test")
)

type CmdBuilder struct {
	cmd        *exec.Cmd
	cancelFunc context.CancelFunc
}

func NewCmdBuilder(command string, args ...string) *CmdBuilder {
	ctx, cancelFunc := context.WithTimeout(context.Background(), 15*time.Minute)
	return &CmdBuilder{cmd: exec.CommandContext(ctx, command, args...), cancelFunc: cancelFunc}
}

func NewTygerCmdBuilder(args ...string) *CmdBuilder {
	return NewCmdBuilder("tyger", args...)
}

func (b *CmdBuilder) Dir(dir string) *CmdBuilder {
	b.cmd.Dir = dir
	return b
}

func (b *CmdBuilder) Env(key string, value string) *CmdBuilder {
	b.cmd.Env = append(b.cmd.Env, fmt.Sprintf("%s=%s", key, value))
	return b
}

func (b *CmdBuilder) Arg(arg string) *CmdBuilder {
	b.cmd.Args = append(b.cmd.Args, arg)
	return b
}

func (b *CmdBuilder) Stdin(stdin string) *CmdBuilder {
	b.cmd.Stdin = bytes.NewBufferString(stdin)
	return b
}

func (b *CmdBuilder) Run() (stdout string, stderr string, err error) {
	defer b.cancelFunc()

	return runCommandCore(b.cmd)
}

func (b *CmdBuilder) RunSucceeds(t *testing.T) string {
	t.Helper()

	stdout, stderr, err := b.Run()
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			t.Log(stderr)
			t.Log(stdout)
			t.Errorf("Unexpected error code %d", exitError.ExitCode())
			t.FailNow()
		}
		t.Errorf("Failure executing %s: %v", b.cmd.String(), err)
		t.FailNow()
	}

	return stdout
}

func runCommand(command string, args ...string) (stdout string, stderr string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, command, args...)

	return runCommandCore(cmd)
}

func runCommandSucceeds(t *testing.T, command string, args ...string) string {
	t.Helper()
	stdout, stderr, err := runCommand(command, args...)
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			t.Log(stderr)
			t.Log(stdout)
			t.Errorf("Unexpected error code %d", exitError.ExitCode())
			t.FailNow()
		}
		t.Errorf("Failure executing %s: %v", command, err)
		t.FailNow()
	}

	return stdout
}

func runCommandCore(cmd *exec.Cmd) (stdout string, stderr string, err error) {
	var outb, errb bytes.Buffer
	cmd.Stdout = &outb
	cmd.Stderr = &errb
	err = cmd.Run()

	// strip away newline suffix
	stdout = string(bytes.TrimSuffix(outb.Bytes(), []byte{'\n'}))

	stderr = string(errb.String())
	return
}

func runTyger(args ...string) (stdout string, stderr string, err error) {
	args = append([]string{"--log-level", "trace"}, args...)
	return runCommand("tyger", args...)
}

func runTygerSucceeds(t *testing.T, args ...string) string {
	t.Helper()
	args = append([]string{"--log-level", "trace"}, args...)
	return runCommandSucceeds(t, "tyger", args...)
}

func runTygerSucceedsUnmarshal[TOut any](t *testing.T, args ...string) TOut {
	t.Helper()
	outString := runTygerSucceeds(t, args...)
	var out TOut
	require.NoError(t, json.Unmarshal([]byte(outString), &out))
	return out
}

func getRun(t *testing.T, runId string) model.Run {
	t.Helper()
	return runTygerSucceedsUnmarshal[model.Run](t, "run", "show", runId)
}

func listRuns(t *testing.T, args ...string) []model.Run {
	t.Helper()
	return runTygerSucceedsUnmarshal[[]model.Run](t, append([]string{"run", "list"}, args...)...)
}

func getRunCounts(t *testing.T, args ...string) map[string]int {
	t.Helper()
	return runTygerSucceedsUnmarshal[map[string]int](t, append([]string{"run", "count"}, args...)...)
}

func getBuffer(t *testing.T, bufferId string, args ...string) model.Buffer {
	t.Helper()
	return runTygerSucceedsUnmarshal[model.Buffer](t, append([]string{"buffer", "show", bufferId}, args...)...)
}

func setRun(t *testing.T, runId string, args ...string) (model.Run, error) {
	t.Helper()
	stdOut, stdErr, err := runTyger(append([]string{"run", "set", runId}, args...)...)
	if err != nil {
		if stdErr != "" {
			return model.Run{}, errors.New(stdErr)
		}
		return model.Run{}, err
	}

	run := model.Run{}
	require.NoError(t, json.Unmarshal([]byte(stdOut), &run))
	return run, nil
}

func setBuffer(t *testing.T, bufferId string, args ...string) (model.Buffer, error) {
	t.Helper()
	stdOut, stdErr, err := runTyger(append([]string{"buffer", "set", bufferId}, args...)...)
	if err != nil {
		if stdErr != "" {
			return model.Buffer{}, errors.New(stdErr)
		}
		return model.Buffer{}, err
	}

	buffer := model.Buffer{}
	require.NoError(t, json.Unmarshal([]byte(stdOut), &buffer))
	return buffer, nil
}

func listBuffers(t *testing.T, args ...string) []model.Buffer {
	t.Helper()
	return runTygerSucceedsUnmarshal[[]model.Buffer](t, append([]string{"buffer", "list"}, args...)...)
}

func getCloudConfig(t *testing.T) *cloudinstall.CloudEnvironmentConfig {
	config := cloudinstall.CloudEnvironmentConfig{}
	require.NoError(t, yaml.UnmarshalStrict([]byte(runCommandSucceeds(t, "../../scripts/get-config.sh")), &config))
	return &config
}

func getDevConfig(t *testing.T) map[string]any {
	config := make(map[string]any)
	require.NoError(t, yaml.UnmarshalStrict([]byte(runCommandSucceeds(t, "../../scripts/get-config.sh", "--dev")), &config))
	return config
}

func getServiceMetadata(t *testing.T) model.ServiceMetadata {
	t.Helper()
	metadata := model.ServiceMetadata{}
	_, err := controlplane.InvokeRequest(context.Background(), http.MethodGet, "/metadata", nil, nil, &metadata)
	require.NoError(t, err)
	return metadata
}

func hasCapability(t *testing.T, capability string) bool {
	t.Helper()
	metadata := getServiceMetadata(t)
	for _, capabilityString := range metadata.Capabilities {
		if capabilityString == capability {
			return true
		}
	}

	return false
}

func supportsNodePools(t *testing.T) bool {
	return hasCapability(t, "NodePools")
}

func supportsEphemeralBuffers(t *testing.T) bool {
	return hasCapability(t, "EphemeralBuffers")
}

func skipIfEphemeralBuffersNotSupported(t *testing.T) {
	if !supportsEphemeralBuffers(t) {
		t.Skip("EphemeralBuffers capability not supported")
	}
}

func skipIfNodePoolsNotSupported(t *testing.T) {
	if !supportsNodePools(t) {
		t.Skip("NodePools capability not supported")
	}
}

func skipIfGpuNotSupported(t *testing.T) {
	if !hasCapability(t, "Gpu") {
		t.Skip("Gpu capability not supported")
	}
}

func supportsDistributedRuns(t *testing.T) bool {
	return hasCapability(t, "DistributedRuns")
}

func skipIfDistributedRunsNotSupported(t *testing.T) {
	if !supportsDistributedRuns(t) {
		t.Skip("DistributedRuns capability not supported")
	}
}

func isUsingUnixSocketDirectlyOrIndirectly() bool {
	if c, _ := controlplane.GetClientFromCache(); c.ControlPlaneUrl.Scheme == "http+unix" {
		return true
	}
	return false
}

func isUsingUnixSocketDirectly() bool {
	if c, _ := controlplane.GetClientFromCache(); c.RawControlPlaneUrl.Scheme == "http+unix" {
		return true
	}
	return false
}

func skipIfUsingUnixSocket(t *testing.T) {
	if isUsingUnixSocketDirectlyOrIndirectly() {
		t.Skip("Skipping test because the control plane is using a local Unix socket")
	}
}

func skipUnlessUsingUnixSocket(t *testing.T) {
	if !isUsingUnixSocketDirectlyOrIndirectly() {
		t.Skip("Skipping test because the control plane is not using a local Unix socket")
	}
}

func skipIfNotUsingUnixSocketDirectly(t *testing.T) {
	if !isUsingUnixSocketDirectly() {
		t.Skip("Skipping test because the control plane is not using a local Unix socket directly")
	}
}

func skipIfNotUsingSSH(t *testing.T) {
	if c, _ := controlplane.GetClientFromCache(); c.ConnectionType() != client.TygerConnectionTypeSsh {
		t.Skip("Skipping test because the control plane is not using SSH")
	}
}

func skipIfOnlyFastTests(t *testing.T) {
	if *runOnlyFastTestsFlag {
		t.Skip("Skipping test because --fast flag is set")
	}
}

func getRoleAssignments(t *testing.T) []string {
	t.Helper()
	c, err := controlplane.GetClientFromCache()
	if err != nil {
		t.Fatalf("Failed to get control plane client: %v", err)
	}

	roles, err := c.GetRoleAssignments(context.Background())
	if err != nil {
		t.Fatalf("Failed to get role assignments: %v", err)
	}

	return roles
}

func skipIfNotOwner(t *testing.T) {
	t.Helper()
	metadata := getServiceMetadata(t)
	if metadata.RbacEnabled {
		roles := getRoleAssignments(t)
		if !slices.Contains(roles, "owner") {
			t.Skip("Skipping test because the user is not an owner")
		}
	}
}

func getLamnaOrgConfig(config *cloudinstall.CloudEnvironmentConfig) *cloudinstall.OrganizationConfig {
	for _, org := range config.Organizations {
		if org.Name == "lamna" {
			return org
		}
	}
	panic("lamna org not found in config")
}
