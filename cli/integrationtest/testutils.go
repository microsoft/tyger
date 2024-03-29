// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build integrationtest

package integrationtest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"testing"
	"time"

	"github.com/microsoft/tyger/cli/internal/install"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
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

func getConfig(t *testing.T) install.EnvironmentConfig {
	config := install.EnvironmentConfig{}
	require.NoError(t, yaml.UnmarshalStrict([]byte(runCommandSucceeds(t, "../../scripts/get-config.sh")), &config))
	return config
}

func getDevConfig(t *testing.T) map[string]any {
	config := make(map[string]any)
	require.NoError(t, yaml.UnmarshalStrict([]byte(runCommandSucceeds(t, "../../scripts/get-config.sh", "--dev")), &config))
	return config
}
