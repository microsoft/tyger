//go:build integrationtest

package integrationtest

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"
)

func runCommand(command string, args ...string) (stdout string, stderr string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, command, args...)

	return runCommandCore(cmd)
}

func runCommandSuceeds(t *testing.T, command string, args ...string) string {
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

func runTygerSuceeds(t *testing.T, args ...string) string {
	args = append([]string{"--log-level", "trace"}, args...)
	return runCommandSuceeds(t, "tyger", args...)
}
