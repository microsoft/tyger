//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"
)

func RunCommand(command string, args ...string) (stdout string, stderr string, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, command, args...)

	var outb, errb bytes.Buffer
	cmd.Stdout = &outb
	cmd.Stderr = &errb
	err = cmd.Run()

	// strip away newline suffix
	stdout = string(bytes.TrimSuffix(outb.Bytes(), []byte{'\n'}))

	stderr = string(errb.String())
	return
}

func RunCommandSuceeds(t *testing.T, command string, args ...string) string {
	stdout, stderr, err := RunCommand(command, args...)
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

func RunTyger(args ...string) (stdout string, stderr string, err error) {
	args = append([]string{"-v"}, args...)
	return RunCommand("tyger", args...)
}

func RunTygerSuceeds(t *testing.T, args ...string) string {
	args = append([]string{"-v"}, args...)
	return RunCommandSuceeds(t, "tyger", args...)
}
