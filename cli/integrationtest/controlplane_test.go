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
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/andreyvit/diff"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/google/uuid"
	"github.com/microsoft/tyger/cli/internal/common"
	"github.com/microsoft/tyger/cli/internal/controlplane"
	"github.com/microsoft/tyger/cli/internal/controlplane/model"
	"github.com/microsoft/tyger/cli/internal/dataplane"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

const (
	BasicImage = "mcr.microsoft.com/azurelinux/base/core:3.0"
	GpuImage   = "nvidia/cuda:11.0.3-base-ubuntu20.04"
	AzCliImage = "mcr.microsoft.com/azure-cli:2.64.0"
)

func init() {
	stdout, stderr, err := runTyger("login", "status")
	if err != nil {
		fmt.Fprintln(os.Stderr, stderr, stdout)
		log.Fatal().Err(err).Send()
	}

	log.Logger = log.Logger.Level(zerolog.ErrorLevel)

	if c, _ := controlplane.GetClientFromCache(); c.ControlPlaneUrl.Scheme == "http+unix" {
		for _, image := range []string{BasicImage, GpuImage, AzCliImage} {
			if _, _, err := runCommand("docker", "inspect", image); err != nil {
				if stdout, stderr, err := runCommand("docker", "pull", image); err != nil {
					fmt.Fprintln(os.Stderr, stderr, stdout)
					log.Fatal().Err(err).Send()
				}
			}
		}
	}
}

func TestEndToEnd(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	// create a codespec
	const codespecName = "testcodespec"

	runTygerSucceeds(t,
		"codespec",
		"create", codespecName,
		"-i=input", "-o=output",
		"--image", BasicImage,
		"--command",
		"--",
		"sh", "-c",
		`
		set -euo pipefail
		inp=$(cat "$INPUT_PIPE")
		echo "${inp}: Bonjour" > "$OUTPUT_PIPE"
		`,
	)

	// create an input buffer and a SAS token to be able to write to it
	inputBufferId := runTygerSucceeds(t, "buffer", "create")
	inputSasUrl := runTygerSucceeds(t, "buffer", "access", inputBufferId, "-w")

	// create and output buffer and a SAS token to be able to read from it
	outputBufferId := runTygerSucceeds(t, "buffer", "create")
	outputSasUrl := runTygerSucceeds(t, "buffer", "access", outputBufferId)

	runCommandSucceeds(t, "sh", "-c", fmt.Sprintf(`echo "Hello" | tyger buffer write "%s"`, inputSasUrl))

	// create run
	runId := runTygerSucceeds(t, "run", "create", "--codespec", codespecName, "--timeout", "10m",
		"-b", fmt.Sprintf("input=%s", inputBufferId),
		"-b", fmt.Sprintf("output=%s", outputBufferId))

	run := waitForRunSuccess(t, runId)

	output := runCommandSucceeds(t, "sh", "-c", fmt.Sprintf(`tyger buffer read "%s"`, outputSasUrl))

	require.Equal("Hello: Bonjour", output)

	require.NotNil(run.StartedAt)
	require.GreaterOrEqual(*run.StartedAt, run.CreatedAt)
	require.NotNil(run.FinishedAt)
	require.GreaterOrEqual(*run.FinishedAt, *run.StartedAt)
}

func TestEndToEndWithAutomaticallyCreatedBuffers(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	// create a codespec
	const codespecName = "testcodespecwithbuffercreation"

	runTygerSucceeds(t,
		"codespec",
		"create", codespecName,
		"-i=input", "-o=output",
		"--image", BasicImage,
		"--command",
		"--",
		"sh", "-c",
		`
		set -euo pipefail
		inp=$(cat "$INPUT_PIPE")
		echo "${inp}: Bonjour" > "$OUTPUT_PIPE"
		`,
	)

	// create run
	runId := runTygerSucceeds(t, "run", "create", "--codespec", codespecName, "--timeout", "10m", "--tag", "testName=TestEndToEndWithAutomaticallyCreatedBuffers")

	run := getRun(t, runId)
	inputBufferId := run.Job.Buffers["input"]
	outputBufferId := run.Job.Buffers["output"]

	runCommandSucceeds(t, "sh", "-c", fmt.Sprintf(`echo "Hello" | tyger buffer write "%s"`, inputBufferId))

	waitForRunSuccess(t, runId)

	output := runCommandSucceeds(t, "sh", "-c", fmt.Sprintf(`tyger buffer read "%s"`, outputBufferId))

	require.Equal("Hello: Bonjour", output)
}

func TestStatusAfterFinalization(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	// create a codespec
	const codespecName = "testcodespecwithbuffercreation"

	runTygerSucceeds(t,
		"codespec",
		"create", codespecName,
		"-i=input", "-o=output",
		"--image", BasicImage,
		"--command",
		"--",
		"sh", "-c",
		`
		set -euo pipefail
		inp=$(cat "$INPUT_PIPE")
		echo "${inp}: Bonjour" > "$OUTPUT_PIPE"
		`,
	)

	// create run
	runId := runTygerSucceeds(t, "run", "create", "--codespec", codespecName, "--timeout", "10m")
	run := getRun(t, runId)

	inputBufferId := run.Job.Buffers["input"]
	outputBufferId := run.Job.Buffers["output"]

	runCommandSucceeds(t, "sh", "-c", fmt.Sprintf(`echo "Hello" | tyger buffer write "%s"`, inputBufferId))

	waitForRunSuccess(t, runId)

	output := runCommandSucceeds(t, "sh", "-c", fmt.Sprintf(`tyger buffer read "%s"`, outputBufferId))

	require.Equal("Hello: Bonjour", output)

	// force logs to be archived
	_, err := controlplane.InvokeRequest(context.Background(), http.MethodPost, "/runs/_sweep", nil, nil, nil)
	require.Nil(err)

	// force finalization
	_, err = controlplane.InvokeRequest(context.Background(), http.MethodPost, "/runs/_sweep", nil, nil, nil)
	require.Nil(err)

	// get run
	run = getRun(t, runId)
	require.Equal(model.Succeeded.String(), run.Status.String())
}

func TestEndToEndWithYamlSpecAndAutomaticallyCreatedBuffers(t *testing.T) {
	t.Parallel()
	require := require.New(t)

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
  tags:
    testName: TestEndToEndWithYamlSpecAndAutomaticallyCreatedBuffers
timeoutSeconds: 600`, BasicImage)

	tempDir := t.TempDir()
	runSpecPath := filepath.Join(tempDir, "runspec.yaml")
	require.NoError(os.WriteFile(runSpecPath, []byte(runSpec), 0644))

	// create run
	runId := runTygerSucceeds(t, "run", "create", "--file", runSpecPath)
	run := getRun(t, runId)

	inputBufferId := run.Job.Buffers["input"]
	inputSasUrl := runTygerSucceeds(t, "buffer", "access", inputBufferId, "-w")
	outputBufferId := run.Job.Buffers["output"]
	outputSasUrl := runTygerSucceeds(t, "buffer", "access", outputBufferId)

	runCommandSucceeds(t, "sh", "-c", fmt.Sprintf(`echo "Hello" | tyger buffer write "%s"`, inputSasUrl))

	waitForRunSuccess(t, runId)

	output := runCommandSucceeds(t, "sh", "-c", fmt.Sprintf(`tyger buffer read "%s"`, outputSasUrl))

	require.Equal("Hello: Bonjour", output)
}

func TestEndToEndExecWithYamlSpec(t *testing.T) {
	t.Parallel()
	require := require.New(t)

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
        echo -n "${inp}: Bonjour" > "$OUTPUT_PIPE"
  tags:
    testName: TestEndToEndExecWithYamlSpec
timeoutSeconds: 600`, BasicImage)

	tempDir := t.TempDir()
	runSpecPath := filepath.Join(tempDir, "runspec.yaml")
	require.NoError(os.WriteFile(runSpecPath, []byte(runSpec), 0644))

	execStdOut := NewTygerCmdBuilder("run", "exec", "--file", runSpecPath, "--log-level", "trace").
		Stdin("Hello").
		RunSucceeds(t)

	require.Equal("Hello: Bonjour", execStdOut)
}

func TestEndToEndExecWithEphemeralBuffers(t *testing.T) {
	t.Parallel()
	skipIfEphemeralBuffersNotSupported(t)
	require := require.New(t)

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
        echo -n "${inp}: Bonjour" > "$OUTPUT_PIPE"

  buffers:
    input: _
    output: _
timeoutSeconds: 600`, BasicImage)

	tempDir := t.TempDir()
	runSpecPath := filepath.Join(tempDir, "runspec.yaml")
	require.NoError(os.WriteFile(runSpecPath, []byte(runSpec), 0644))

	execStdOut := NewTygerCmdBuilder("run", "exec", "--file", runSpecPath, "--log-level", "trace").
		Stdin("Hello").
		RunSucceeds(t)

	require.Equal("Hello: Bonjour", execStdOut)
}

func TestEndToEndExecWithLargeEphemeralBuffers(t *testing.T) {
	t.Parallel()
	skipIfEphemeralBuffersNotSupported(t)
	require := require.New(t)

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
        cat "$INPUT_PIPE" > "$OUTPUT_PIPE"

  buffers:
    input: _
    output: _
timeoutSeconds: 600`, BasicImage)

	tempDir := t.TempDir()
	runSpecPath := filepath.Join(tempDir, "runspec.yaml")
	require.NoError(os.WriteFile(runSpecPath, []byte(runSpec), 0644))

	genCmd := exec.Command("tyger", "buffer", "gen", "1G")
	genPipe, err := genCmd.StdoutPipe()
	require.NoError(err)

	execCmd := exec.Command("tyger", "run", "exec", "--file", runSpecPath)
	execCmd.Stdin = genPipe

	stdErr := &bytes.Buffer{}
	execCmd.Stderr = stdErr

	execOutPipe, err := execCmd.StdoutPipe()
	require.NoError(err)

	genCmd.Start()
	execCmd.Start()

	outByteCount := 0
	for {
		buf := make([]byte, 64*1024)
		n, err := execOutPipe.Read(buf)
		outByteCount += n
		if err == io.EOF {
			break
		}
		require.NoError(err)
	}

	execErr := execCmd.Wait()
	t.Log(stdErr.String())
	require.NoError(execErr)
	require.NoError(genCmd.Wait())

	require.Equal(1*1024*1024*1024, outByteCount)
}

func TestEndToEndExecWithSockets(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	runSpec := fmt.Sprintf(`
job:
  codespec:
    image: %s
    buffers:
      inputs: ["input"]
      outputs: ["output"]
    sockets:
      - port: 9002
        inputBuffer: input
        outputBuffer: output
    args:
      - socket
      - --port
      - "9002"
timeoutSeconds: 600`, getTestConnectivityImage(t))

	tempDir := t.TempDir()
	runSpecPath := filepath.Join(tempDir, "runspec.yaml")
	require.NoError(os.WriteFile(runSpecPath, []byte(runSpec), 0644))

	execStdOut := NewTygerCmdBuilder("run", "exec", "--file", runSpecPath, "--log-level", "trace").
		Stdin("0123").
		RunSucceeds(t)

	require.Equal("1234", execStdOut)
}

func TestEndToEndExecWithSocketsWithDelay(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	runSpec := fmt.Sprintf(`
job:
  codespec:
    image: %s
    buffers:
      inputs: ["input"]
      outputs: ["output"]
    sockets:
      - port: 9002
        inputBuffer: input
        outputBuffer: output
    args:
      - socket
      - --port
      - "9002"
      - --delay
      - 5s
timeoutSeconds: 600`, getTestConnectivityImage(t))

	tempDir := t.TempDir()
	runSpecPath := filepath.Join(tempDir, "runspec.yaml")
	require.NoError(os.WriteFile(runSpecPath, []byte(runSpec), 0644))

	execStdOut, execStdErr, err := NewTygerCmdBuilder("run", "exec", "--file", runSpecPath, "--logs", "--log-level", "trace").
		Stdin("0123").
		Run()

	require.NoError(err)
	require.Equal("1234", execStdOut)
	require.NotContains(strings.ToLower(execStdErr), "timed out waiting for logs")
}

func TestEndToEndExecWithSocketsAndEphemeralBuffers(t *testing.T) {
	t.Parallel()
	skipIfEphemeralBuffersNotSupported(t)
	require := require.New(t)

	runSpec := fmt.Sprintf(`
job:
  codespec:
    image: %s
    buffers:
      inputs: ["input"]
      outputs: ["output"]
    sockets:
      - port: 9002
        inputBuffer: input
        outputBuffer: output
    args:
      - socket
      - --port
      - "9002"
  buffers:
    input: _
    output: _
timeoutSeconds: 600`, getTestConnectivityImage(t))

	tempDir := t.TempDir()
	runSpecPath := filepath.Join(tempDir, "runspec.yaml")
	require.NoError(os.WriteFile(runSpecPath, []byte(runSpec), 0644))

	execStdOut := NewTygerCmdBuilder("run", "exec", "--file", runSpecPath, "--logs", "--log-level", "trace").
		Stdin("0123").
		RunSucceeds(t)

	require.Equal("1234", execStdOut)
}

func TestEndToEndCreateWithShortBufferAccessTtl(t *testing.T) {
	t.Parallel()
	skipIfOnlyFastTests(t)
	require := require.New(t)

	scriptPath, err := filepath.Abs("slow_copy.py")
	require.Nil(err)
	scriptBytes, err := os.ReadFile(scriptPath)
	require.Nil(err)

	const codespecName = "testcreatewithbufferaccessttl"

	runTygerSucceeds(t,
		"codespec",
		"create", codespecName,
		"-i=input", "-o=output",
		"--image", AzCliImage,
		"--command",
		"--",
		"/bin/bash", "-c",
		fmt.Sprintf(`
set -euo pipefail
cat << EOF > slow-copy.py
%s
EOF
cat $(INPUT_PIPE) | python3 slow-copy.py > $(OUTPUT_PIPE)
`, string(scriptBytes)),
	)

	inputBufferId := runTygerSucceeds(t, "buffer", "create")
	outputBufferId := runTygerSucceeds(t, "buffer", "create")

	genCmd := exec.Command("tyger", "buffer", "gen", "150M")
	genPipe, err := genCmd.StdoutPipe()
	require.NoError(err)

	writeCmd := exec.Command("tyger", "buffer", "write", inputBufferId, "--access-ttl", "0.00:01:00")
	writeCmd.Stdin = genPipe

	require.NoError(genCmd.Start())
	require.NoError(writeCmd.Run())
	require.NoError(genCmd.Wait())

	runId := runTygerSucceeds(t, "run", "create", "--codespec", codespecName, "--buffer-access-ttl", "0.00:01:00", "--buffer", "input="+inputBufferId, "--buffer", "output="+outputBufferId, "--timeout", "10m")

	run := getRun(t, runId)
	require.Equal(run.BufferAccessTtl, "00:01:00")

	readCmd := exec.Command("tyger", "buffer", "read", outputBufferId, "--access-ttl", "0.00:01:00")
	readOutPipe, err := readCmd.StdoutPipe()
	require.NoError(err)
	require.NoError(readCmd.Start())

	outByteCount := 0
	for {
		buf := make([]byte, 64*1024)
		n, err := readOutPipe.Read(buf)
		outByteCount += n
		if err == io.EOF {
			break
		}
		require.NoError(err)
	}

	require.NoError(readCmd.Wait())
	require.Equal(150*1024*1024, outByteCount)

	waitForRunSuccess(t, runId)
}

func TestEndToEndExecWithShortBufferAccessTtl(t *testing.T) {
	t.Parallel()
	skipIfOnlyFastTests(t)

	scriptPath, err := filepath.Abs("slow_copy.py")
	require.Nil(t, err)
	scriptBytes, err := os.ReadFile(scriptPath)
	require.Nil(t, err)

	const codespecName = "testexecwithbufferaccessttl"

	runTygerSucceeds(t,
		"codespec",
		"create", codespecName,
		"-i=input", "-o=output",
		"--image", AzCliImage,
		"--command",
		"--",
		"/bin/bash", "-c",
		fmt.Sprintf(`
set -euo pipefail
cat << EOF > slow-copy.py
%s
EOF
cat $(INPUT_PIPE) | python3 slow-copy.py > $(OUTPUT_PIPE)
`, string(scriptBytes)),
	)

	testCases := []struct {
		name      string
		ephemeral bool
		args      []string
	}{
		{"auto-created-buffers", false, nil},
		{"ephemeral-buffers", true, []string{"--buffer", "input=_", "--buffer", "output=_"}},
	}
	for _, tC := range testCases {
		tC := tC
		t.Run(tC.name, func(t *testing.T) {
			t.Parallel()
			if tC.ephemeral {
				skipIfEphemeralBuffersNotSupported(t)
			}

			require := require.New(t)

			genCmd := exec.Command("tyger", "buffer", "gen", "150M")
			genPipe, err := genCmd.StdoutPipe()
			require.NoError(err)

			args := []string{"run", "exec", "--codespec", codespecName, "--buffer-access-ttl", "0.00:01:00", "--log-level", "trace", "--timeout", "10m"}
			if tC.args != nil {
				args = append(args, tC.args...)
			}
			execCmd := exec.Command("tyger", args...)
			execCmd.Stdin = genPipe

			stdErr := &bytes.Buffer{}
			execCmd.Stderr = stdErr

			execOutPipe, err := execCmd.StdoutPipe()
			require.NoError(err)

			genCmd.Start()
			execCmd.Start()

			outByteCount := 0
			for {
				buf := make([]byte, 64*1024)
				n, err := execOutPipe.Read(buf)
				outByteCount += n
				if err == io.EOF {
					break
				}
				require.NoError(err)
			}

			execErr := execCmd.Wait()
			require.NoError(execErr)
			require.NoError(genCmd.Wait())

			require.Equal(150*1024*1024, outByteCount)
		})
	}
}

func TestInvalidImage(t *testing.T) {
	t.Parallel()
	skipIfUsingUnixSocket(t)

	require := require.New(t)

	missingImage := BasicImage + "thisisamissingtag"

	runSpec := fmt.Sprintf(`
job:
  codespec:
    image: %s
tags:
  testName: TestInvalidImage
timeoutSeconds: 600`, missingImage)

	tempDir := t.TempDir()
	runSpecPath := filepath.Join(tempDir, "runspec.yaml")
	require.NoError(os.WriteFile(runSpecPath, []byte(runSpec), 0644))

	_, stdErr, err := runTyger("run", "exec", "--file", runSpecPath)
	require.Error(err)
	require.Contains(stdErr, fmt.Sprintf("%s: not found", missingImage))
}

func TestCodespecBufferTagsWithYamlSpec(t *testing.T) {
	t.Parallel()
	require := require.New(t)

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
        echo -n "${inp}: Bonjour" > "$OUTPUT_PIPE"
timeoutSeconds: 600`, BasicImage)

	tempDir := t.TempDir()
	runSpecPath := filepath.Join(tempDir, "runspec.yaml")
	require.NoError(os.WriteFile(runSpecPath, []byte(runSpec), 0644))

	uniqueId := uuid.New().String()

	execStdOut := NewTygerCmdBuilder("run", "exec", "--file", runSpecPath, "--buffer-tag", "testName=TestCodespecBufferTagsWithYamlSpec", "--buffer-tag", "testtagX="+uniqueId, "--log-level", "trace").
		Stdin("Hello").
		RunSucceeds(t)

	require.Equal("Hello: Bonjour", execStdOut)

	buffers := listBuffers(t, "--tag", "testName=TestCodespecBufferTagsWithYamlSpec", "--tag", "testtagX="+uniqueId)
	require.Equal(2, len(buffers))
}

func TestCodespecBufferTtlWithYamlSpec(t *testing.T) {
	t.Parallel()
	require := require.New(t)

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
        echo -n "${inp}: Bonjour" > "$OUTPUT_PIPE"
timeoutSeconds: 600`, BasicImage)

	tempDir := t.TempDir()
	runSpecPath := filepath.Join(tempDir, "runspec.yaml")
	require.NoError(os.WriteFile(runSpecPath, []byte(runSpec), 0644))

	uniqueId := uuid.New().String()

	execStdOut := NewTygerCmdBuilder("run", "exec", "--file", runSpecPath, "--buffer-ttl", "0.00:05", "--buffer-tag", "testName=TestCodespecBufferTtlWithYamlSpec", "--buffer-tag", "testtagX="+uniqueId, "--log-level", "trace").
		Stdin("Hello").
		RunSucceeds(t)

	require.Equal("Hello: Bonjour", execStdOut)

	buffers := listBuffers(t, "--tag", "testName=TestCodespecBufferTtlWithYamlSpec", "--tag", "testtagX="+uniqueId)
	require.Equal(2, len(buffers))
	for _, buffer := range buffers {
		require.Greater(*buffer.ExpiresAt, time.Now())
		require.Less(*buffer.ExpiresAt, time.Now().Add(10*time.Minute))
	}
}

func TestEndToEndExecWithYamlWithExistingCodespec(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	codespecName := strings.ToLower(t.Name())
	version := runTygerSucceeds(t,
		"codespec",
		"create", codespecName,
		"-i=input", "-o=output",
		"--image", BasicImage,
		"--command",
		"--",
		"sh", "-c",
		`
		set -euo pipefail
		inp=$(cat "$INPUT_PIPE")
		echo -n "${inp}: Bonjour" > "$OUTPUT_PIPE"
		`,
	)

	runSpec := fmt.Sprintf(`
job:
  codespec: %s/versions/%s
timeoutSeconds: 600`, codespecName, version)

	tempDir := t.TempDir()
	runSpecPath := filepath.Join(tempDir, "runspec.yaml")
	require.NoError(os.WriteFile(runSpecPath, []byte(runSpec), 0644))

	execStdOut := NewTygerCmdBuilder("run", "exec", "--file", runSpecPath, "--tag", "testName=TestEndToEndExecWithYamlWithExistingCodespec", "--log-level", "trace").
		Stdin("Hello").
		RunSucceeds(t)
	require.Equal("Hello: Bonjour", execStdOut)
}

func TestEndToEndWhenPipesAreNotTouched(t *testing.T) {
	t.Parallel()
	require := require.New(t)

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
        echo "hello world"
timeoutSeconds: 600`, BasicImage)

	tempDir := t.TempDir()
	runSpecPath := filepath.Join(tempDir, "runspec.yaml")
	require.NoError(os.WriteFile(runSpecPath, []byte(runSpec), 0644))

	execStdOut := NewTygerCmdBuilder("run", "exec", "--file", runSpecPath, "--log-level", "trace").
		Stdin("Hello").
		RunSucceeds(t)

	require.Empty(execStdOut)
}
func TestCreateCodespecsWithSpecFile(t *testing.T) {
	t.Parallel()

	codespecName := strings.ToLower(t.Name() + "1")

	spec := fmt.Sprintf(`
name: %s
buffers:
  inputs:
    - input
  outputs:
    - output
image: quay.io/linuxserver.io/ffmpeg
command: ["ffmpeg"]
args: ["1", "2", "3"]
workingDir: /some/path
env:
  MY_VAR: myValue
resources:
  requests:
    cpu: 1
    memory: 1G
  limits:
    cpu: 2
    memory: 2G
  gpu: 1
maxReplicas: 1
`, codespecName)

	tempDir := t.TempDir()
	specPath := filepath.Join(tempDir, "spec.yaml")
	require.NoError(t, os.WriteFile(specPath, []byte(spec), 0644))
	parsedSpec := model.Codespec{}
	require.NoError(t, yaml.Unmarshal([]byte(spec), &parsedSpec))

	runTygerSucceeds(t, "codespec", "create", "-f", specPath)

	receivedSpecString := runTygerSucceeds(t, "codespec", "show", codespecName)
	var receivedSpec model.Codespec
	require.NoError(t, json.Unmarshal([]byte(receivedSpecString), &receivedSpec))

	require.Equal(t, codespecName, *receivedSpec.Name)
	require.Equal(t, parsedSpec.Buffers, receivedSpec.Buffers)
	require.Equal(t, parsedSpec.Image, receivedSpec.Image)
	require.Equal(t, parsedSpec.Command, receivedSpec.Command)
	require.Equal(t, parsedSpec.Args, receivedSpec.Args)
	require.Equal(t, parsedSpec.WorkingDir, receivedSpec.WorkingDir)
	require.Equal(t, parsedSpec.Env, receivedSpec.Env)
	require.Equal(t, parsedSpec.Resources, receivedSpec.Resources)
	require.Equal(t, parsedSpec.MaxReplicas, receivedSpec.MaxReplicas)

	// now override the spec name
	codespec2Name := strings.ToLower(t.Name() + "2")
	runTygerSucceeds(t, "codespec", "create", codespec2Name, "-f", specPath)

	receivedSpecString = runTygerSucceeds(t, "codespec", "show", codespec2Name)
	require.NoError(t, json.Unmarshal([]byte(receivedSpecString), &receivedSpec))

	require.Equal(t, codespec2Name, *receivedSpec.Name)

	// now override the spec name and image
	codespec3Name := strings.ToLower(t.Name() + "3")
	runTygerSucceeds(t, "codespec", "create", codespec3Name, "-f", specPath, "--image", "ubuntu")

	receivedSpecString = runTygerSucceeds(t, "codespec", "show", codespec3Name)
	require.NoError(t, json.Unmarshal([]byte(receivedSpecString), &receivedSpec))

	require.Equal(t, codespec3Name, *receivedSpec.Name)
	require.Equal(t, "ubuntu", receivedSpec.Image)
}
func TestInvalidCodespecNames(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name  string
		valid bool
	}{
		{"foo", true},
		{"foo-1_2.9", true},
		{"Foo", false},
		{"foo bar", false},
	}
	for _, tC := range testCases {
		t.Run(tC.name, func(t *testing.T) {
			_, stdErr, err := runTyger("codespec", "create", tC.name, "--image", BasicImage)
			if tC.valid {
				require.NoError(t, err)
			} else {
				require.NotNil(t, err)
				require.Contains(t, stdErr, "codespec name")
			}

			newCodespec := model.Codespec{Kind: "worker", Image: BasicImage}
			_, err = controlplane.InvokeRequest(context.Background(), http.MethodPut, fmt.Sprintf("/codespecs/%s", tC.name), nil, newCodespec, nil)
			if tC.valid {
				require.Nil(t, err)
			} else {
				require.NotNil(t, err)
			}
		})
	}
}

func TestInvalidBufferNames(t *testing.T) {
	t.Parallel()
	testCases := []string{
		"FOO",
		"foo_bar",
		"-foo",
		"bar-",
	}
	for _, tC := range testCases {
		t.Run(tC, func(t *testing.T) {
			_, stdErr, err := runTyger("codespec", "create", "testinvalidbuffernames", "--image", BasicImage, "--input", tC)
			require.NotNil(t, err)
			require.Contains(t, stdErr, "Buffer names must consist")
		})
	}
}

func TestInvalidBufferNamesInInlineCodespec(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	runSpec := fmt.Sprintf(`
job:
  codespec:
    image: %s
    buffers:
      inputs: ["INVALID_NAME"]
timeoutSeconds: 600`, BasicImage)

	tempDir := t.TempDir()
	runSpecPath := filepath.Join(tempDir, "runspec.yaml")
	require.NoError(os.WriteFile(runSpecPath, []byte(runSpec), 0644))

	_, stdErr, err := runTyger("run", "exec", "--file", runSpecPath)
	require.NotNil(err)
	require.Contains(stdErr, "Buffer names must consist")
}

// Verify that a run using a codespec that requires a GPU
// is scheduled on a node with one.
func TestGpuResourceRequirement(t *testing.T) {
	t.Parallel()
	skipIfOnlyFastTests(t)
	skipIfGpuNotSupported(t)

	const codespecName = "gputestcodespec"
	runTygerSucceeds(t,
		"codespec",
		"create", codespecName,
		"--image", GpuImage,
		"--gpu", "1",
		"--command",
		"--",
		"bash", "-c", "[[ $(nvidia-smi -L | wc -l) == 1 ]]") // verify that a GPU is available

	// create run
	runId := runTygerSucceeds(t, "run", "create", "--codespec", codespecName, "--timeout", "10m")

	run := waitForRunSuccess(t, runId)
	if supportsNodePools(t) {
		require.NotEmpty(t, run.Cluster)
		require.Equal(t, "gpunp", run.Job.NodePool)
	}
}

// Verify that a run using a codespec that does not require a GPU
// is not scheduled on a node with one.
func TestNoGpuResourceRequirement(t *testing.T) {
	t.Parallel()

	const codespecName = "nogputestcodespec"
	runTygerSucceeds(t,
		"codespec",
		"create", codespecName,
		"--image", GpuImage,
		"--command",
		"--",
		"bash", "-c", "[[ ! $(nvidia-smi) ]]") // verify that no GPU is available

	// create run
	runId := runTygerSucceeds(t, "run", "create", "--codespec", codespecName, "--timeout", "10m")

	waitForRunSuccess(t, runId)
}

func TestTargetGpuNodePool(t *testing.T) {
	t.Parallel()
	skipIfOnlyFastTests(t)
	skipIfNodePoolsNotSupported(t)

	codespecName := strings.ToLower(t.Name())
	runTygerSucceeds(t,
		"codespec",
		"create", codespecName,
		"--image", GpuImage,
		"--command",
		"--",
		"bash", "-c", "[[ $(nvidia-smi -L | wc -l) == 1 ]]") // verify that a GPU is available

	// create run
	runId := runTygerSucceeds(t, "run", "create", "--codespec", codespecName, "--node-pool", "gpunp", "--timeout", "20m")

	waitForRunSuccess(t, runId)
}

func TestTargetCpuNodePool(t *testing.T) {
	t.Parallel()

	codespecName := strings.ToLower(t.Name())
	runTygerSucceeds(t,
		"codespec",
		"create", codespecName,
		"--image", GpuImage,
		"--command",
		"--",
		"bash", "-c", "[[ ! $(nvidia-smi) ]]") // verify that no GPU is available

	// create run
	runId := runTygerSucceeds(t, "run", "create", "--codespec", codespecName, "--node-pool", "cpunp", "--timeout", "10m")

	waitForRunSuccess(t, runId)
}

func TestTargetingInvalidClusterReturnsError(t *testing.T) {
	t.Parallel()
	skipIfNodePoolsNotSupported(t)

	codespecName := strings.ToLower(t.Name())
	runTygerSucceeds(t,
		"codespec",
		"create", codespecName,
		"--image", BasicImage)

	_, stderr, _ := runTyger("run", "create", "--codespec", codespecName, "--cluster", "invalid")
	require.Contains(t, stderr, "Unknown cluster")
}

func TestTargetingInvalidNodePoolReturnsError(t *testing.T) {
	t.Parallel()
	skipIfNodePoolsNotSupported(t)

	codespecName := strings.ToLower(t.Name())
	runTygerSucceeds(t,
		"codespec",
		"create", codespecName,
		"--image", BasicImage)

	_, stderr, _ := runTyger("run", "create", "--codespec", codespecName, "--node-pool", "invalid")
	require.Contains(t, stderr, "Unknown nodepool")
}

func TestTargetCpuNodePoolWithGpuResourcesReturnsError(t *testing.T) {
	t.Parallel()
	skipIfNodePoolsNotSupported(t)

	codespecName := strings.ToLower(t.Name())
	runTygerSucceeds(t,
		"codespec",
		"create", codespecName,
		"--image", BasicImage,
		"--gpu", "1")

	_, stderr, _ := runTyger("run", "create", "--codespec", codespecName, "--node-pool", "cpunp", "--timeout", "10m")
	require.Contains(t, stderr, "does not have GPUs and cannot satisfy GPU request")
}

func TestUnrecognizedFieldsRejected(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	codespec := model.Codespec{}
	requestBody := map[string]string{"kind": "job", "image": "x"}
	_, err := controlplane.InvokeRequest(context.Background(), http.MethodPut, "/codespecs/tcs", nil, requestBody, &codespec)
	require.Nil(err)

	requestBody["unknownField"] = "y"
	_, err = controlplane.InvokeRequest(context.Background(), http.MethodPut, "/codespecs/tcs", nil, requestBody, &codespec)
	require.NotNil(err)
}

func TestInvalidCodespecDiscriminatorRejected(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	codespec := model.Codespec{}
	requestBody := map[string]string{"image": "x"}
	_, err := controlplane.InvokeRequest(context.Background(), http.MethodPut, "/codespecs/tcs", nil, requestBody, &codespec)
	require.ErrorContains(err, "Missing discriminator property 'kind'")

	requestBody["kind"] = "missing"
	_, err = controlplane.InvokeRequest(context.Background(), http.MethodPut, "/codespecs/tcs", nil, requestBody, &codespec)
	require.ErrorContains(err, "Invalid value for the property 'kind'. It can be either 'job' or 'worker'")
}

func TestInvalidCodespecMissingRequiredFieldsRejected(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	codespec := model.Codespec{}
	requestBody := map[string]string{"kind": "job"}
	_, err := controlplane.InvokeRequest(context.Background(), http.MethodPut, "/codespecs/tcs", nil, requestBody, &codespec)
	require.ErrorContains(err, "missing required properties including: 'image'")
}

func TestResponseContainsRequestIdHeader(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	_, stderr, _ := runTyger("codespec", "show", "missing")

	require.Contains(stderr, "Request-Id")
}

func TestOpenApiSpecIsAsExpected(t *testing.T) {
	t.Parallel()
	client, err := controlplane.GetClientFromCache()
	require.NoError(t, err)

	swaggerUrl := fmt.Sprintf("%s/swagger/v1.0/swagger.yaml", client.ControlPlaneUrl)
	t.Logf("swaggerUrl: %s", swaggerUrl)
	resp, err := client.ControlPlaneClient.Get(swaggerUrl)
	require.Nil(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	actualBytes, err := io.ReadAll(resp.Body)
	require.Nil(t, err)
	expectedFilePath, err := filepath.Abs("expected_openapi_spec.yaml")
	require.Nil(t, err)
	expectedBytes, err := os.ReadFile(expectedFilePath)
	require.Nil(t, err)

	if a, e := strings.TrimSpace(string(actualBytes)), strings.TrimSpace(string(expectedBytes)); a != e {
		var curlCommand string
		if client.ControlPlaneUrl.Scheme == "http+unix" {
			u := url.URL{
				Scheme: "http",
				Host:   "localhost",
				Path:   strings.Split(swaggerUrl, ":")[2],
			}

			curlCommand = fmt.Sprintf("curl --unix %s %s ", strings.Split(client.ControlPlaneUrl.Path, ":")[0], u.String())
		} else {
			curlCommand = fmt.Sprintf("curl %s ", swaggerUrl)
		}

		t.Errorf("Result not as expected.\n\nDiff: %v\n\nTo update, run `%s > %s`",
			diff.LineDiff(e, a),
			curlCommand,
			expectedFilePath,
		)
	}
}

func TestRunStatusEnumUnmarshal(t *testing.T) {

	expectedFilePath, err := filepath.Abs("expected_openapi_spec.yaml")
	require.Nil(t, err)

	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = true

	doc, err := loader.LoadFromFile(expectedFilePath)
	require.Nil(t, err)

	err = doc.Validate(loader.Context)
	require.Nil(t, err)

	for _, value := range doc.Components.Schemas["Run"].Value.Properties["status"].Value.Enum {
		var testStatus model.RunStatus
		err = testStatus.UnmarshalJSON([]byte("\"" + value.(string) + "\""))
		require.Nil(t, err)
	}
}

func TestListRunsPaging(t *testing.T) {
	t.Parallel()

	runTygerSucceeds(t,
		"codespec",
		"create", "exitimmediately",
		"--image", BasicImage,
		"--command",
		"--",
		"echo", "hi")

	runs := make(map[string]string)
	for i := 0; i < 10; i++ {
		runs[runTygerSucceeds(t, "run", "create", "--codespec", "exitimmediately", "--timeout", "10m")] = ""
	}

	for url := "/runs?limit=5"; url != ""; {
		page := model.Page[model.Run]{}
		_, err := controlplane.InvokeRequest(context.Background(), http.MethodGet, url, nil, nil, &page)
		require.Nil(t, err)
		for _, r := range page.Items {
			delete(runs, fmt.Sprint(r.Id))
			if len(runs) == 0 {
				return
			}
		}

		if page.NextLink == "" {
			break
		}

		url = strings.TrimLeft(page.NextLink, "/")
	}

	require.Empty(t, runs)
}

func TestListCodespecsFromCli(t *testing.T) {
	t.Parallel()
	prefix := strings.ToLower(t.Name()) + "_"
	codespecNames := [4]string{prefix + "kspace_half_sampled", prefix + "4dcardiac", prefix + "zloc_10mm", prefix + "axial_1mm"}
	codespecMap := make(map[string]string)
	for _, name := range codespecNames {
		codespecMap[name] = runTygerSucceeds(t, "codespec", "create", name, "--image", BasicImage)
	}
	var results = runTygerSucceeds(t, "codespec", "list", "--prefix", prefix)
	var returnedCodespecs []model.Codespec
	json.Unmarshal([]byte(results), &returnedCodespecs)
	sort.Strings(codespecNames[:])
	var csIdx int = 0
	for _, cs := range returnedCodespecs {
		if _, ok := codespecMap[*cs.Name]; ok {
			require.Equal(t, codespecNames[csIdx], *cs.Name)
			require.Equal(t, codespecMap[*cs.Name], strconv.Itoa(*cs.Version))
			csIdx++
		}
	}
	require.Equal(t, len(codespecNames), csIdx)
}

func TestRecreateCodespec(t *testing.T) {
	t.Parallel()
	codespecName := strings.ToLower(t.Name() + uuid.NewString())
	version1 := runTygerSucceeds(t, "codespec", "create", codespecName, "--image", "busybee", "--command", "--", "echo", "hi I am first")
	version2 := runTygerSucceeds(t, "codespec", "create", codespecName, "--image", BasicImage, "--gpu", "2", "--memory-request", "2048048", "--env", "os=ubuntu", "--command", "--", "echo", "hi I am latest")
	require.NotEqual(t, version1, version2)

	version3 := runTygerSucceeds(t, "codespec", "create", codespecName, "--image", BasicImage, "--gpu", "2", "--memory-request", "2048048", "--env", "os=ubuntu", "--command", "--", "echo", "hi I am latest")
	require.Equal(t, version2, version3)

	version4 := runTygerSucceeds(t, "codespec", "create", codespecName, "--image", BasicImage, "--memory-request", "2048048", "--gpu", "2", "--env", "os=ubuntu", "--command", "--", "echo", "hi I am latest")
	require.Equal(t, version3, version4)

	version5 := runTygerSucceeds(t, "codespec", "create", codespecName, "--image", BasicImage, "--memory-request", "2048048", "--gpu", "2", "--env", "os=ubuntu", "--env", "platform=highT", "--command", "--", "echo", "hi I am latest")
	version6 := runTygerSucceeds(t, "codespec", "create", codespecName, "--image", BasicImage, "--gpu", "2", "--memory-request", "2048048", "--env", "platform=highT", "--env", "os=ubuntu", "--command", "--", "echo", "hi I am latest")
	require.Equal(t, version5, version6)

	version7 := runTygerSucceeds(t, "codespec", "create", codespecName, "--image", BasicImage, "--memory-request", "2048048", "--gpu", "2", "--env", "platform=highT", "--env", "os=windows", "--command", "--", "echo", "hi I am latest")
	require.NotEqual(t, version6, version7)
}

func TestListCodespecsPaging(t *testing.T) {
	t.Parallel()

	prefix := strings.ToLower(t.Name()+uuid.NewString()) + "_"
	inputNames := [12]string{"klamath", "allagash", "middlefork", "johnday", "missouri", "riogrande", "chattooga", "loxahatchee", "noatak", "tuolumne", "riogrande", "allagash"}
	expectedNames1 := [5]string{"allagash", "chattooga", "johnday", "klamath", "loxahatchee"}
	expectedNames2 := [5]string{"middlefork", "missouri", "noatak", "riogrande", "tuolumne"}
	for i := range inputNames {
		inputNames[i] = prefix + inputNames[i]
	}
	for i := range expectedNames1 {
		expectedNames1[i] = prefix + expectedNames1[i]
	}
	for i := range expectedNames2 {
		expectedNames2[i] = prefix + expectedNames2[i]
	}

	var returnedNames1, returnedNames2 [5]string
	var expectedIdx, currentKlamathVersion, expectedKlamathVersion int = 0, 0, 0

	codespecs := make(map[string]string)
	for _, name := range inputNames {
		codespecs[name] = runTygerSucceeds(t, "codespec", "create", name, "--image", BasicImage)
	}
	require.Equal(t, len(codespecs), 10)

	for url := fmt.Sprintf("/codespecs?limit=5&prefix=%s", prefix); url != ""; {
		page := model.Page[model.Codespec]{}
		_, err := controlplane.InvokeRequest(context.Background(), http.MethodGet, url, nil, nil, &page)
		require.Nil(t, err)
		for _, cs := range page.Items {
			if _, ok := codespecs[*cs.Name]; ok {
				if expectedIdx < 5 {
					returnedNames1[expectedIdx] = *cs.Name
					expectedIdx++
					if *cs.Name == prefix+"klamath" {
						currentKlamathVersion = *cs.Version
					}
				} else {
					returnedNames2[expectedIdx-5] = *cs.Name
					expectedIdx++
				}
			}
			//simulate concurrent codespec update while paging
			if expectedIdx == 6 && expectedKlamathVersion == 0 {
				var tmp = runTygerSucceeds(t, "codespec", "create", prefix+"klamath", "--image", BasicImage, "--", "something different")
				expectedKlamathVersion, err = strconv.Atoi(tmp)
				require.Nil(t, err)
				require.Equal(t, expectedKlamathVersion, currentKlamathVersion+1)
			}
			if expectedIdx > 10 {
				require.Fail(t, "Unexpected codespec count")
			}
		}

		if page.NextLink == "" {
			break
		}

		url = strings.TrimLeft(page.NextLink, "/")
	}

	require.Equal(t, expectedNames1, returnedNames1)
	require.Equal(t, expectedNames2, returnedNames2)
}

func TestListRunsSince(t *testing.T) {
	t.Parallel()

	codespecName := strings.ToLower(t.Name())

	runTygerSucceeds(t,
		"codespec",
		"create", codespecName,
		"--image", BasicImage,
		"--command",
		"--",
		"echo", "hi")

	runTygerSucceeds(t, "run", "create", "--codespec", codespecName, "--timeout", "10m")
	midId := runTygerSucceeds(t, "run", "create", "--codespec", codespecName, "--timeout", "10m")
	lastId := runTygerSucceeds(t, "run", "create", "--codespec", codespecName, "--timeout", "10m")

	midRun := getRun(t, midId)

	listJson := runTygerSucceeds(t, "run", "list", "--since", midRun.CreatedAt.Format(time.RFC3339Nano))
	list := make([]model.Run, 0)
	json.Unmarshal([]byte(listJson), &list)
	require.Greater(t, len(list), 0)
	for _, r := range list {
		require.Greater(t, r.CreatedAt.UnixNano(), midRun.CreatedAt.UnixNano())
	}

	for _, r := range list {
		if fmt.Sprint(r.Id) == lastId {
			return
		}
	}

	require.Fail(t, "last run not found")
}

func TestListAndCountRunsWithFilters(t *testing.T) {
	t.Parallel()

	tag1 := fmt.Sprintf("testtag1=%s", uuid.NewString())
	tag2 := fmt.Sprintf("testtag2=%s", uuid.NewString())

	successfulCodespecName := strings.ToLower(t.Name()) + "success"
	runTygerSucceeds(t,
		"codespec",
		"create", successfulCodespecName,
		"--image", BasicImage,
		"--command",
		"--",
		"echo", "hi")

	runTygerSucceeds(t, "run", "create", "--codespec", successfulCodespecName, "--timeout", "10m")
	succeedingTestId := runTygerSucceeds(t, "run", "create", "--codespec", successfulCodespecName, "--timeout", "10m", "--tag", tag1, "--tag", tag2)

	hangingCodespecName := strings.ToLower(t.Name()) + "hang"
	runTygerSucceeds(t,
		"codespec",
		"create", hangingCodespecName,
		"--image", BasicImage,
		"--command",
		"--",
		"sleep", "60")

	runTygerSucceeds(t, "run", "create", "--codespec", hangingCodespecName, "--timeout", "10m")
	hangingRunId := runTygerSucceeds(t, "run", "create", "--codespec", hangingCodespecName, "--timeout", "10m", "--tag", tag1, "--tag", tag2)

	successRun := waitForRunSuccess(t, succeedingTestId)
	hangingRun := waitForRunStarted(t, hangingRunId)

	timeBeforeFirstRun := successRun.CreatedAt.Add(-time.Millisecond)
	timeAfterFirstRun := successRun.CreatedAt.Add(time.Millisecond)
	timeAfterSecondRun := hangingRun.CreatedAt.Add(time.Millisecond)

	listResult := listRuns(t, "--tag", tag1, "--tag", tag2)
	require.Len(t, listResult, 2)

	listResult = listRuns(t, "--tag", tag1, "--tag", tag2, "--since", timeBeforeFirstRun.Format(time.RFC3339Nano))
	require.Len(t, listResult, 2)

	listResult = listRuns(t, "--tag", tag1, "--tag", tag2, "--since", timeAfterFirstRun.Format(time.RFC3339Nano))
	require.Len(t, listResult, 1)

	listResult = listRuns(t, "--tag", tag1, "--tag", tag2, "--since", timeAfterSecondRun.Format(time.RFC3339Nano))
	require.Len(t, listResult, 0)

	listResult = listRuns(t, "--tag", tag1)
	require.Len(t, listResult, 2)

	listResult = listRuns(t, "--tag", tag1, "--tag", "x=y")
	require.Len(t, listResult, 0)

	listResult = listRuns(t, "--tag", tag1, "--tag", tag2, "--status", "succeeded")
	require.Len(t, listResult, 1)

	listResult = listRuns(t, "--tag", tag1, "--tag", tag2, "--status", "succeeded", "--status", "running")
	require.Len(t, listResult, 2)

	listResult = listRuns(t, "--tag", tag1, "--tag", tag2, "--status", "succeeded", "--status", "running", "--status", "canceled")
	require.Len(t, listResult, 2)

	counts := getRunCounts(t, "--tag", tag1, "--tag", tag2)
	require.Len(t, counts, 2)
	require.Equal(t, 1, counts["succeeded"])
	require.Equal(t, 1, counts["running"])

	counts = getRunCounts(t, "--tag", tag1, "--tag", tag2, "--since", timeBeforeFirstRun.Format(time.RFC3339Nano))
	require.Len(t, counts, 2)
	require.Equal(t, 1, counts["succeeded"])
	require.Equal(t, 1, counts["running"])

	counts = getRunCounts(t, "--tag", tag1, "--tag", tag2, "--since", timeAfterFirstRun.Format(time.RFC3339Nano))
	require.Len(t, counts, 1)
	require.Equal(t, 1, counts["running"])

	counts = getRunCounts(t, "--tag", tag1, "--tag", tag2, "--since", timeAfterSecondRun.Format(time.RFC3339Nano))
	require.Len(t, counts, 0)

	counts = getRunCounts(t, "--tag", tag1)
	require.Len(t, counts, 2)
	require.Equal(t, 1, counts["succeeded"])
	require.Equal(t, 1, counts["running"])

	counts = getRunCounts(t, "--tag", tag1, "--tag", "x=y")
	require.Len(t, counts, 0)
}

func TestListCodespecsWithPrefix(t *testing.T) {
	t.Parallel()

	codespecNames := [4]string{"3d_t2_flair", "t1w-1mm-ax", "t1w-0.9mm-sag", "3d_t1_star"}
	codespecMap := make(map[string]string)
	for i := 0; i < 4; i++ {
		codespecMap[codespecNames[i]] = runTygerSucceeds(t, "codespec", "create", codespecNames[i], "--image", BasicImage)
	}

	url := "/codespecs?prefix=3d_"
	page := model.Page[model.Codespec]{}
	_, err := controlplane.InvokeRequest(context.Background(), http.MethodGet, url, nil, nil, &page)
	require.Nil(t, err)
	for _, cs := range page.Items {
		require.Equal(t, strings.HasPrefix(*cs.Name, "3d_"), true)
		delete(codespecMap, *cs.Name)
	}
	require.Equal(t, len(codespecMap), 2)

	for cs := range codespecMap {
		require.Equal(t, strings.HasPrefix(cs, "t1w-"), true)
	}
}

func TestGetLogsFromPod(t *testing.T) {
	t.Parallel()

	codespecName := strings.ToLower(t.Name())

	runTygerSucceeds(t,
		"codespec",
		"create", codespecName,
		"--image", BasicImage,
		"--command",
		"--",
		"sh", "-c", "for i in `seq 1 5`; do echo $i; done; sleep 30")

	runId := runTygerSucceeds(t, "run", "create", "--codespec", codespecName, "--timeout", "10m")
	t.Logf("Run ID: %s", runId)

	waitForRunStarted(t, runId)

	// block until we get the first line
	resp, err := controlplane.InvokeRequest(context.Background(), http.MethodGet, fmt.Sprintf("/runs/%s/logs?follow=true", runId), nil, nil, nil, controlplane.WithLeaveResponseOpen())
	require.Nil(t, err)
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	for i := 0; i < 5; i++ {
		_, err = reader.ReadString('\n')
		require.Nil(t, err)
	}

	logs := runTygerSucceeds(t, "run", "logs", runId)
	require.Equal(t, "1\n2\n3\n4\n5", logs)

	// --timestamp should prefix each line with its timestamp
	logs = runTygerSucceeds(t, "run", "logs", runId, "--timestamps")
	lines := strings.Split(logs, "\n")
	require.Equal(t, 5, len(lines))
	var firstTimestamp time.Time
	for i := len(lines) - 1; i >= 0; i-- {
		firstTimestamp, err = time.Parse(time.RFC3339Nano, strings.Split(lines[i], " ")[0])
		require.Nil(t, err)
	}

	// --since one second later. The kubernetes API appears to have a 1-second resolution when evaluating sinceTime
	logs = runTygerSucceeds(t, "run", "logs", runId, "--since", firstTimestamp.Add(time.Second).Format(time.RFC3339Nano))
	require.NotContains(t, logs, "1")

	logs = runTygerSucceeds(t, "run", "logs", runId, "--tail", "3")
	require.Equal(t, "3\n4\n5", logs)
}

func TestGetArchivedLogs(t *testing.T) {
	t.Parallel()

	codespecName := strings.ToLower(t.Name())

	runTygerSucceeds(t,
		"codespec",
		"create", codespecName,
		"--image", BasicImage,
		"--command",
		"--",
		"sh", "-c", "echo 1; sleep 1; echo 2; sleep 1; echo 3; sleep 1; echo 4;")

	runId := runTygerSucceeds(t, "run", "create", "--codespec", codespecName, "--timeout", "10m")
	t.Logf("Run ID: %s", runId)
	waitForRunStarted(t, runId)
	logs := runTygerSucceeds(t, "run", "logs", runId, "--follow")
	require.Equal(t, "1\n2\n3\n4", logs)

	waitForRunSuccess(t, runId)

	// force logs to be archived
	_, err := controlplane.InvokeRequest(context.Background(), http.MethodPost, "/runs/_sweep", nil, nil, nil)
	require.Nil(t, err)

	logs = runTygerSucceeds(t, "run", "logs", runId)
	require.Equal(t, "1\n2\n3\n4", logs)

	// --timestamp should prefix each line with its timestamp
	logs = runTygerSucceeds(t, "run", "logs", runId, "--timestamps")
	lines := strings.Split(logs, "\n")
	require.Equal(t, 4, len(lines))
	var firstTimestamp time.Time
	for i := len(lines) - 1; i >= 0; i-- {
		firstTimestamp, err = time.Parse(time.RFC3339Nano, strings.Split(lines[i], " ")[0])
		require.Nil(t, err)
	}

	// --since
	logs = runTygerSucceeds(t, "run", "logs", runId, "--since", firstTimestamp.Format(time.RFC3339Nano))
	require.Equal(t, "2\n3\n4", logs)
	logs = runTygerSucceeds(t, "run", "logs", runId, "--since", firstTimestamp.Add(time.Minute).Format(time.RFC3339Nano))
	require.Equal(t, "", logs)
	logs = runTygerSucceeds(t, "run", "logs", runId, "--since", firstTimestamp.Add(-time.Minute).Format(time.RFC3339Nano))
	require.Equal(t, "1\n2\n3\n4", logs)

	// --tail
	logs = runTygerSucceeds(t, "run", "logs", runId, "--tail", "3")
	require.Equal(t, "2\n3\n4", logs)
	logs = runTygerSucceeds(t, "run", "logs", runId, "--tail", "0")
	require.Equal(t, "", logs)
	logs = runTygerSucceeds(t, "run", "logs", runId, "--tail", "4")
	require.Equal(t, "1\n2\n3\n4", logs)
}

func TestGetArchivedLogsWithLongLines(t *testing.T) {
	t.Parallel()

	codespecName := strings.ToLower(t.Name())

	runTygerSucceeds(t,
		"codespec",
		"create", codespecName,
		"--image", BasicImage,
		"--command",
		"--",
		"sh", "-c", `head -c 2000000 < /dev/zero | tr '\0' 'a'; echo ""; sleep 1; head -c 2000000 < /dev/zero | tr '\0' 'b';`)

	expectedLogs := strings.Repeat("a", 2000000) + "\n" + strings.Repeat("b", 2000000)

	runId := runTygerSucceeds(t, "run", "create", "--codespec", codespecName, "--timeout", "10m")
	t.Logf("Run ID: %s", runId)
	waitForRunStarted(t, runId)
	logs := runTygerSucceeds(t, "run", "logs", runId, "--follow")
	require.Equal(t, expectedLogs, logs)

	// force logs to be archived
	_, err := controlplane.InvokeRequest(context.Background(), http.MethodPost, "/runs/_sweep", nil, nil, nil)
	require.Nil(t, err)

	logs = runTygerSucceeds(t, "run", "logs", runId)
	require.Equal(t, expectedLogs, logs)
}

func TestConnectivityBetweenJobAndWorkers(t *testing.T) {
	t.Parallel()
	skipIfDistributedRunsNotSupported(t)

	jobCodespecName := strings.ToLower(t.Name()) + "-job"
	workerCodespecName := strings.ToLower(t.Name()) + "-worker"

	digest := getTestConnectivityImage(t)

	runTygerSucceeds(t,
		"codespec",
		"create", jobCodespecName,
		"--image", digest,
		"--",
		"job")

	runTygerSucceeds(t,
		"codespec",
		"create", workerCodespecName,
		"--kind", "worker",
		"--image", digest,
		"--max-replicas", "3",
		"--endpoint", "TestWorker=29477",
		"--",
		"worker")

	runId := runTygerSucceeds(t, "run", "create", "--codespec", jobCodespecName, "--worker-codespec", workerCodespecName, "--worker-replicas", "3", "--timeout", "10m")
	waitForRunSuccess(t, runId)
}

func TestAuthenticationRequired(t *testing.T) {
	t.Parallel()
	if getServiceMetadata(t).Authority == "" {
		t.Skip("Authentication disabled for this server")
	}

	client, err := controlplane.GetClientFromCache()
	require.NoError(t, err)
	resp, err := client.ControlPlaneClient.Get(fmt.Sprintf("%s/runs/abc", client.ControlPlaneUrl))
	require.Nil(t, err)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestSpecifyingCacheFileAsEnvironmentVariable(t *testing.T) {
	_, stdErr, err := NewTygerCmdBuilder("login", "status").
		Env("", ""). // a non-nil environment means that the this process's environment is not used
		Run()

	require.Error(t, err)
	require.Contains(t, stdErr, "run 'tyger login' to connect to a Tyger server")

	cachePath, err := controlplane.GetCachePath()
	require.NoError(t, err)

	NewTygerCmdBuilder("login", "status").
		Env("TYGER_CACHE_FILE", cachePath).
		RunSucceeds(t)
}

func TestCancelRun(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	codespecName := strings.ToLower(t.Name())

	runTygerSucceeds(t,
		"codespec",
		"create", codespecName,
		"--image", getTestConnectivityImage(t),
		"--",
		"worker")

	runId := runTygerSucceeds(t, "run", "create", "--codespec", codespecName, "--timeout", "10m")
	t.Logf("Run ID: %s", runId)

	runTygerSucceeds(t, "run", "cancel", runId)

	waitForRunCanceled(t, runId)

	// Check that the run failed because it was canceled.
	run := getRun(t, runId)

	require.Equal(model.Canceled, *run.Status)
	require.Equal("Canceled by user", run.StatusReason)
}

func TestCancelTerminatesOutputBuffers(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	runSpec := fmt.Sprintf(`
job:
  codespec:
    image: %s
    buffers:
      outputs: ["output"]
    command:
      - sleep
      - 10m
timeoutSeconds: 600`, BasicImage)

	tempDir := t.TempDir()
	runSpecPath := filepath.Join(tempDir, "runspec.yaml")
	require.NoError(os.WriteFile(runSpecPath, []byte(runSpec), 0644))

	// create run
	runId := runTygerSucceeds(t, "run", "create", "--file", runSpecPath)
	run := getRun(t, runId)

	runTygerSucceeds(t, "run", "cancel", runId)

	outputBufferId := run.Job.Buffers["output"]
	out, stdErr, err := runTyger("buffer", "read", outputBufferId)
	require.Len(out, 0)

	// There is a race condition based on whether the pod starts before the cancel request is processed.
	// If the pod was started, the the buffer should be marked as completed because of the INT signal handler.
	// If not, it will be marked as failed.

	if err != nil {
		require.Contains(stdErr, dataplane.ErrBufferFailedState.Error())
	}
}

func TestBufferWithoutTags(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	bufferId := runTygerSucceeds(t, "buffer", "create")
	t.Logf("Buffer ID: %s", bufferId)

	buffer := getBuffer(t, bufferId)
	require.Equal(0, len(buffer.Tags))
}

func TestBufferWithTags(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	bufferId := runTygerSucceeds(t, "buffer", "create", "--tag", "testtag1=testvalue1", "--tag", "testtag2=testvalue2")
	t.Logf("Buffer ID: %s", bufferId)

	buffer := getBuffer(t, bufferId)

	require.Equal(2, len(buffer.Tags))
	require.Equal("testvalue1", buffer.Tags["testtag1"])
	require.Equal("testvalue2", buffer.Tags["testtag2"])
}

func TestCreateBufferWithTtl(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	bufferJson := runTygerSucceeds(t, "buffer", "create", "--ttl", "2.12:30:30", "--full-resource")

	var buffer model.Buffer
	require.NoError(json.Unmarshal([]byte(bufferJson), &buffer))

	require.NotNil(buffer.ExpiresAt)
	require.Greater(*buffer.ExpiresAt, time.Now().Add((2*24+12)*time.Hour+30*time.Minute))
	require.Less(*buffer.ExpiresAt, time.Now().Add((2*24+12)*time.Hour+31*time.Minute))
}

func TestBufferSetTags(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	bufferId := runTygerSucceeds(t, "buffer", "create", "--tag", "testtag1=testvalue1", "--tag", "testtag2=testvalue2")
	t.Logf("Buffer ID: %s", bufferId)

	buffer, err := setBuffer(t, bufferId, "--tag", "testtag2=testvalue2updated", "--tag", "testtag3=testvalue3")
	require.NoError(err)

	require.Equal(map[string]string{"testtag1": "testvalue1", "testtag2": "testvalue2updated", "testtag3": "testvalue3"}, buffer.Tags)
}

func TestBufferSetTagsWithClear(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	bufferId := runTygerSucceeds(t, "buffer", "create", "--tag", "testtag1=testvalue1", "--tag", "testtag2=testvalue2")
	t.Logf("Buffer ID: %s", bufferId)

	buffer, err := setBuffer(t, bufferId, "--clear-tags", "--tag", "testtag3=testvalue3", "--tag", "testtag4=testvalue4")
	require.NoError(err)

	require.Equal(map[string]string{"testtag3": "testvalue3", "testtag4": "testvalue4"}, buffer.Tags)
}

func TestBufferSetTagsClearWithETag(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	bufferJson := runTygerSucceeds(t, "buffer", "create", "--tag", "testtag1=testvalue1", "--tag", "testtag2=testvalue2", "--full-resource")

	var bufferETag model.Buffer
	require.NoError(json.Unmarshal([]byte(bufferJson), &bufferETag))

	t.Logf("Buffer ID: %s eTag: %s", bufferETag.Id, bufferETag.ETag)

	buffer, err := setBuffer(t, bufferETag.Id, "--clear-tags", "--tag", "testtag3=testvalue3", "--tag", "testtag4=testvalue4", "--etag", bufferETag.ETag)
	require.NoError(err)

	require.Equal(map[string]string{"testtag3": "testvalue3", "testtag4": "testvalue4"}, buffer.Tags)
	require.NotEqual(bufferETag.ETag, buffer.ETag)
}

func TestBufferSetTagsWithETag(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	bufferJson := runTygerSucceeds(t, "buffer", "create", "--tag", "testtag1=testvalue1", "--tag", "testtag2=testvalue2", "--full-resource")

	var bufferETag model.Buffer
	require.NoError(json.Unmarshal([]byte(bufferJson), &bufferETag))

	t.Logf("Buffer ID: %s eTag: %s", bufferETag.Id, bufferETag.ETag)

	buffer, err := setBuffer(t, bufferETag.Id, "--tag", "testtag2=testvalue2updated", "--tag", "testtag4=testvalue4", "--etag", bufferETag.ETag)
	require.NoError(err)

	require.Equal(map[string]string{"testtag1": "testvalue1", "testtag2": "testvalue2updated", "testtag4": "testvalue4"}, buffer.Tags)
	require.NotEqual(bufferETag.ETag, buffer.ETag)
}

func TestBufferSetTtl(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	bufferId1 := runTygerSucceeds(t, "buffer", "create")
	buffer1 := getBuffer(t, bufferId1)
	require.Nil(buffer1.ExpiresAt)

	buffer1, err := setBuffer(t, bufferId1, "--ttl", "0")
	require.NoError(err)
	require.Less(*buffer1.ExpiresAt, time.Now().Add(time.Second))

	bufferId2 := runTygerSucceeds(t, "buffer", "create", "--tag", "testtag1=setTtl1")
	buffer2, err := setBuffer(t, bufferId2, "--ttl", "2.12:30:30", "--tag", "testtag2=setTtl2")
	buffer2FromShow := getBuffer(t, bufferId2)
	require.NoError(err)
	require.Equal(buffer2FromShow, buffer2)
	require.Greater(*buffer2.ExpiresAt, time.Now().Add((2*24+12)*time.Hour+30*time.Minute))
	require.Less(*buffer2.ExpiresAt, time.Now().Add((2*24+12)*time.Hour+31*time.Minute))
}

func TestBufferSetTtlOnDeletedBuffer(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	bufferId := runTygerSucceeds(t, "buffer", "create")

	runTygerSucceeds(t, "buffer", "delete", bufferId)

	_, stderr, err := runTyger("buffer", "set", bufferId, "--ttl", "0")
	require.Error(err)
	require.Contains(stderr, "not found")

	buffer, err := setBuffer(t, bufferId, "--ttl", "0", "--soft-deleted")
	require.NoError(err)
	require.Less(*buffer.ExpiresAt, time.Now().Add(time.Second))
}

func TestBufferSetTtlWithETag(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	bufferJson := runTygerSucceeds(t, "buffer", "create", "--tag", "testtag1=setTttl1", "--tag", "testtag2=setTtl2", "--full-resource")

	var bufferETag model.Buffer
	require.NoError(json.Unmarshal([]byte(bufferJson), &bufferETag))

	t.Logf("Buffer ID: %s eTag: %s", bufferETag.Id, bufferETag.ETag)

	buffer, err := setBuffer(t, bufferETag.Id, "--ttl", "0", "--tag", "testtag2=setTtl2Updated", "--tag", "testtag4=setTtl4", "--etag", bufferETag.ETag)
	require.NoError(err)

	require.Less(*buffer.ExpiresAt, time.Now().Add(time.Second))
	require.NotEqual(bufferETag.ETag, buffer.ETag)
}

func TestBufferSetWithInvalidETag(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	bufferId := runTygerSucceeds(t, "buffer", "create", "--tag", "testtag1=testvalue1", "--tag", "testtag2=testvalue2")
	t.Logf("Buffer ID: %s", bufferId)

	runTygerSucceeds(t, "buffer", "show", bufferId)

	_, stderr, _ := runTyger("buffer", "set", bufferId, "--etag", "bad-etag")
	require.Contains(stderr, "the server's ETag does not match the provided ETag")

	_, stderr2, _ := runTyger("buffer", "set", "bad-bufferid", "--etag", "bad-etag")
	require.Contains(stderr2, "was not found")

	_, stderr3, _ := runTyger("buffer", "set", bufferId, "--ttl", "1.00:00:00", "--etag", "bad-etag")
	require.Contains(stderr3, "the server's ETag does not match the provided ETag")
}

func TestBufferSetClearTags(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	bufferId := runTygerSucceeds(t, "buffer", "create", "--tag", "testtag1=testvalue1", "--tag", "testtag2=testvalue2")
	t.Logf("Buffer ID: %s", bufferId)

	buffer, err := setBuffer(t, bufferId, "--clear-tags")
	require.NoError(err)
	require.Equal(0, len(buffer.Tags))
}

func TestBufferList(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	uniqueId := uuid.New().String()

	bufferId1 := runTygerSucceeds(t, "buffer", "create", "--tag", "testtag1=testvalue1", "--tag", "testtag2=testvalue2", "--tag", "testtagX="+uniqueId)
	bufferId2 := runTygerSucceeds(t, "buffer", "create", "--tag", "testtag2=testvalue2", "--tag", "testtag3=testvalue3", "--tag", "testtagX="+uniqueId)
	bufferId3 := runTygerSucceeds(t, "buffer", "create", "--tag", "testtag1=testvalue1", "--tag", "testtag3=testvalue3", "--tag", "testtagX="+uniqueId)

	buffers := listBuffers(t, "--tag", "testtag1=testvalue1", "--tag", "testtag2=testvalue2", "--tag", "testtagX="+uniqueId)
	require.Equal(1, len(buffers))
	require.Equal(3, len(buffers[0].Tags))
	require.Equal(bufferId1, buffers[0].Id)
	require.Equal(uniqueId, buffers[0].Tags["testtagX"])
	require.Equal("testvalue1", buffers[0].Tags["testtag1"])
	require.Equal("testvalue2", buffers[0].Tags["testtag2"])

	buffers = listBuffers(t, "--tag", "testtag2=testvalue2", "--tag", "testtag3=testvalue3", "--tag", "testtagX="+uniqueId)
	require.Equal(1, len(buffers))
	require.Equal(3, len(buffers[0].Tags))
	require.Equal(bufferId2, buffers[0].Id)
	require.Equal(uniqueId, buffers[0].Tags["testtagX"])
	require.Equal("testvalue2", buffers[0].Tags["testtag2"])
	require.Equal("testvalue3", buffers[0].Tags["testtag3"])

	buffers = listBuffers(t, "buffer", "list", "--tag", "testtag1=testvalue1", "--tag", "testtag3=testvalue3", "--tag", "testtagX="+uniqueId)
	require.Equal(1, len(buffers))
	require.Equal(3, len(buffers[0].Tags))
	require.Equal(bufferId3, buffers[0].Id)
	require.Equal(uniqueId, buffers[0].Tags["testtagX"])
	require.Equal("testvalue1", buffers[0].Tags["testtag1"])
	require.Equal("testvalue3", buffers[0].Tags["testtag3"])

	buffers = listBuffers(t, "buffer", "list", "--tag", "testtag1=testvalue1", "--tag", "testtagX="+uniqueId)
	require.Equal(2, len(buffers))

	buffers = listBuffers(t, "buffer", "list", "--tag", "testtagX="+uniqueId, "--exclude-tag", "testtag1=testvalue1")
	require.Equal(1, len(buffers))
	require.Equal(3, len(buffers[0].Tags))
	require.Equal(bufferId2, buffers[0].Id)

	buffers = listBuffers(t, "buffer", "list", "--tag", "testtagX="+uniqueId, "--exclude-tag", "testtag1=testvalue1", "--exclude-tag", "testtag2=testvalue2")
	require.Equal(0, len(buffers))
}

func TestBufferListWithLimit(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	uniqueId := uuid.New().String()

	runTygerSucceeds(t, "buffer", "create", "--tag", "testtagX="+uniqueId)
	runTygerSucceeds(t, "buffer", "create", "--tag", "testtagX="+uniqueId)
	runTygerSucceeds(t, "buffer", "create", "--tag", "testtagX="+uniqueId)

	buffers := listBuffers(t, "--limit", "1", "--tag", "testtagX="+uniqueId)
	require.Equal(1, len(buffers))

	buffers = listBuffers(t, "--limit", "2", "--tag", "testtagX="+uniqueId)
	require.Equal(2, len(buffers))

	buffers = listBuffers(t, "--limit", "3", "--tag", "testtagX="+uniqueId)
	require.Equal(3, len(buffers))
}

func TestBufferListWithoutTags(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	bufferJson := runTygerSucceeds(t, "buffer", "create", "--full-resource")
	var buffer model.Buffer
	require.NoError(json.Unmarshal([]byte(bufferJson), &buffer))

	bufferJson = runTygerSucceeds(t, "buffer", "list")
	var buffers []model.Buffer
	require.NoError(json.Unmarshal([]byte(bufferJson), &buffers))

	require.Contains(buffers, buffer)
}

func TestBufferDeleteById(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	bufferId := runTygerSucceeds(t, "buffer", "create")

	bufferJson := runTygerSucceeds(t, "buffer", "delete", bufferId)
	var deletedBuffer model.Buffer
	require.NoError(json.Unmarshal([]byte(bufferJson), &deletedBuffer))

	_, _, err := runTyger("buffer", "show", bufferId)
	assert.Error(t, err)

	buffer := getBuffer(t, bufferId, "--soft-deleted")
	require.Equal(buffer, deletedBuffer)

	bufferJson = runTygerSucceeds(t, "buffer", "restore", bufferId)
	var restoredBuffer model.Buffer
	require.NoError(json.Unmarshal([]byte(bufferJson), &restoredBuffer))

	require.Equal(buffer.Id, restoredBuffer.Id)
	require.Equal(buffer.CreatedAt, restoredBuffer.CreatedAt)

	runTygerSucceeds(t, "buffer", "show", restoredBuffer.Id)
}

func TestBufferDeletionStates(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	bufferId := runTygerSucceeds(t, "buffer", "create")

	// Restore Active - 412
	_, stderr, err := runTyger("buffer", "restore", bufferId)
	require.Error(err)
	require.Contains(stderr, "Buffer is not soft-deleted")

	// Purge Active - 412
	_, stderr, err = runTyger("buffer", "purge", bufferId)
	require.Error(err)
	require.Contains(stderr, "Buffer is not soft-deleted")

	// Delete Active - 200
	deleted := runTygerSucceedsUnmarshal[model.Buffer](t, "buffer", "delete", bufferId)
	require.Equal(bufferId, deleted.Id)

	// Delete Deleted - 412
	_, stderr, err = runTyger("buffer", "delete", bufferId)
	require.Error(err)
	require.Contains(stderr, "Buffer is already soft-deleted")

	// Restore Deleted - 200
	restored := runTygerSucceedsUnmarshal[model.Buffer](t, "buffer", "restore", bufferId)
	require.Equal(bufferId, restored.Id)

	// (Delete again)
	runTygerSucceeds(t, "buffer", "delete", bufferId)

	// Purge Deleted - 200
	purged := runTygerSucceedsUnmarshal[model.Buffer](t, "buffer", "purge", bufferId)
	require.Equal(bufferId, purged.Id)

	// Delete Purged - 404
	_, stderr, err = runTyger("buffer", "delete", bufferId)
	require.Error(err)
	require.Contains(stderr, "not found")

	// Restore Purged - 404
	_, stderr, err = runTyger("buffer", "restore", bufferId)
	require.Error(err)
	require.Contains(stderr, "not found")

	// Purge purged - 404
	_, stderr, err = runTyger("buffer", "purge", bufferId)
	require.Error(err)
	require.Contains(stderr, "not found")
}

func TestBufferDeleteMultipleIds(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	bufferId1 := runTygerSucceeds(t, "buffer", "create")
	bufferId2 := runTygerSucceeds(t, "buffer", "create")

	bufferJson := runTygerSucceeds(t, "buffer", "delete", bufferId1, bufferId2)
	var deleted []model.Buffer
	require.NoError(json.Unmarshal([]byte(bufferJson), &deleted))
	require.Len(deleted, 2)

	for _, buf := range deleted {
		_, _, err := runTyger("buffer", "show", buf.Id)
		assert.Error(t, err)

		shown := getBuffer(t, buf.Id, "--soft-deleted")
		require.Equal(buf, shown)
	}

	bufferJson = runTygerSucceeds(t, "buffer", "restore", bufferId1, bufferId2)
	var restored []model.Buffer
	require.NoError(json.Unmarshal([]byte(bufferJson), &restored))
	require.Len(restored, 2)

	runTygerSucceeds(t, "buffer", "show", bufferId1)
}

func TestBufferDeleteByTag(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	uniqueId := uuid.New().String()

	bufferId1 := runTygerSucceeds(t, "buffer", "create", "--tag", "testtag1=delete1", "--tag", "testtag2=delete2", "--tag", "testtagX="+uniqueId)
	bufferId2 := runTygerSucceeds(t, "buffer", "create", "--tag", "testtag2=delete2", "--tag", "testtag3=delete3", "--tag", "testtagX="+uniqueId)
	bufferId3 := runTygerSucceeds(t, "buffer", "create", "--tag", "testtag1=delete1", "--tag", "testtag3=delete3", "--tag", "testtagX="+uniqueId)

	runTygerSucceeds(t, "buffer", "delete", "--force", "--tag", "testtag1=delete1", "--tag", "testtag2=delete2")
	buffers := listBuffers(t, "--soft-deleted", "--tag", "testtagX="+uniqueId)
	require.Equal(1, len(buffers))
	require.Equal(bufferId1, buffers[0].Id)

	runTygerSucceeds(t, "buffer", "delete", "--force", "--tag", "testtagX="+uniqueId, "--exclude-tag", "testtag1=delete1")
	buffers = listBuffers(t, "--soft-deleted", "--tag", "testtagX="+uniqueId)
	require.Equal(2, len(buffers))
	require.Equal(bufferId2, buffers[0].Id)

	runTygerSucceeds(t, "buffer", "delete", "--force", "--tag", "testtagX="+uniqueId, "--exclude-tag", "asdf=qwer", "--exclude-tag", "zxcv=asdf")
	buffers = listBuffers(t, "--soft-deleted", "--tag", "testtagX="+uniqueId)
	require.Equal(3, len(buffers))
	require.Equal(bufferId3, buffers[0].Id)

	// Now make sure we can restore
	runTygerSucceeds(t, "buffer", "restore", "--force", "--tag", "testtagX="+uniqueId, "--exclude-tag", "testtag3=delete3")
	buffers = listBuffers(t, "--soft-deleted", "--tag", "testtagX="+uniqueId)
	require.Equal(2, len(buffers))

	runTygerSucceeds(t, "buffer", "restore", "--force", "--tag", "testtagX="+uniqueId)
	buffers = listBuffers(t, "--soft-deleted", "--tag", "testtagX="+uniqueId)
	require.Equal(0, len(buffers))

	buffers = listBuffers(t, "--tag", "testtagX="+uniqueId)
	require.Equal(3, len(buffers))
}

func TestBufferDeleteAll(t *testing.T) {
	require := require.New(t)

	bufferId1 := runTygerSucceeds(t, "buffer", "create")
	bufferId2 := runTygerSucceeds(t, "buffer", "create")
	bufferId3 := runTygerSucceeds(t, "buffer", "create")

	runTygerSucceeds(t, "buffer", "delete", "--all", "--force")

	buffer1 := getBuffer(t, bufferId1, "--soft-deleted")
	buffer2 := getBuffer(t, bufferId2, "--soft-deleted")
	buffer3 := getBuffer(t, bufferId3, "--soft-deleted")

	buffers := listBuffers(t, "--soft-deleted")
	require.Contains(buffers, buffer1)
	require.Contains(buffers, buffer2)
	require.Contains(buffers, buffer3)

	runTygerSucceeds(t, "buffer", "restore", "--all", "--force")
	runTygerSucceeds(t, "buffer", "show", bufferId2)
	deleted := listBuffers(t, "--soft-deleted")
	require.Len(deleted, 0)
}

func TestBufferPurge(t *testing.T) {
	t.Parallel()
	skipIfOnlyFastTests(t)

	require := require.New(t)

	bufferId1 := runTygerSucceeds(t, "buffer", "create")
	bufferId2 := runTygerSucceeds(t, "buffer", "create")
	bufferId3 := runTygerSucceeds(t, "buffer", "create", "--tag", "delete=true")
	bufferId4 := runTygerSucceeds(t, "buffer", "create", "--tag", "delete=true", "--tag", "purge=true")
	bufferId5 := runTygerSucceeds(t, "buffer", "create", "--tag", "delete=true", "--tag", "purge=false")

	sasUrl := runTygerSucceeds(t, "buffer", "access", bufferId1, "-w")
	runCommandSucceeds(t, "sh", "-c", fmt.Sprintf(`echo "Hello" | tyger buffer write "%s"`, sasUrl))
	runTygerSucceeds(t, "buffer", "read", sasUrl)

	runTygerSucceeds(t, "buffer", "delete", bufferId1, bufferId2)
	bufferJson := runTygerSucceeds(t, "buffer", "purge", bufferId1, bufferId2)
	var purged []model.Buffer
	require.NoError(json.Unmarshal([]byte(bufferJson), &purged))
	require.Len(purged, 2)
	require.Less(*purged[0].ExpiresAt, time.Now().Add(time.Second))

	// Delete then immediately purge tagged buffers
	runTygerSucceeds(t, "buffer", "delete", "--force", "--tag", "delete=true")
	runTygerSucceeds(t, "buffer", "purge", "--force", "--tag", "delete=true", "--exclude-tag", "purge=false")

	// Wait for buffers to be purged... This depends on the sleep time in BufferDeleter
	time.Sleep(time.Second * 35)

	ids := []string{bufferId1, bufferId2, bufferId3, bufferId4}
	for _, id := range ids {
		_, _, err := runTyger("buffer", "show", id)
		assert.Error(t, err)

		_, _, err = runTyger("buffer", "show", "--soft-deleted", id)
		assert.Error(t, err)
	}

	_, stderr, err := runTyger("buffer", "read", sasUrl)
	require.Error(err)
	require.Contains(stderr, "the buffer does not exist")

	// Check that the --exclude-tag worked
	runTygerSucceeds(t, "buffer", "show", "--soft-deleted", bufferId5)
}

func TestBufferSetTtlTriggersDeleter(t *testing.T) {
	t.Parallel()
	skipIfOnlyFastTests(t)

	require := require.New(t)

	bufferJson := runTygerSucceeds(t, "buffer", "create", "--tag", "testtagX=triggerDeleter", "--full-resource")

	var buffer model.Buffer
	require.NoError(json.Unmarshal([]byte(bufferJson), &buffer))
	require.Nil(buffer.ExpiresAt)

	activeExpiredBuffer, err := setBuffer(t, buffer.Id, "--ttl", "0")
	require.NoError(err)
	require.Less(*activeExpiredBuffer.ExpiresAt, time.Now().Add(time.Second))

	// Wait for the buffer to be soft-deleted
	time.Sleep(time.Second * 35)

	_, _, err = runTyger("buffer", "show", activeExpiredBuffer.Id)
	assert.Error(t, err)

	softDeletedBuffer := getBuffer(t, activeExpiredBuffer.Id, "--soft-deleted")

	deletedExpiredBuffer, err := setBuffer(t, softDeletedBuffer.Id, "--ttl", "0", "--soft-deleted")
	require.NoError(err)
	require.Less(*deletedExpiredBuffer.ExpiresAt, time.Now().Add(time.Second))

	// Wait for the buffer to be purged
	time.Sleep(time.Second * 35)

	_, _, err = runTyger("buffer", "show", deletedExpiredBuffer.Id)
	assert.Error(t, err)
	_, _, err = runTyger("buffer", "show", "--soft-deleted", deletedExpiredBuffer.Id)
	assert.Error(t, err)
}

func TestCreateRunWithDeletedBuffer(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	// create a codespec
	const codespecName = "testcodespec"

	runTygerSucceeds(t,
		"codespec",
		"create", codespecName,
		"-i=input", "-o=output",
		"--image", BasicImage,
		"--command",
		"--",
		"sh", "-c",
		`
		set -euo pipefail
		inp=$(cat "$INPUT_PIPE")
		echo "${inp}: Bonjour" > "$OUTPUT_PIPE"
		`,
	)

	// create input and output buffers
	inputBufferId := runTygerSucceeds(t, "buffer", "create")
	outputBufferId := runTygerSucceeds(t, "buffer", "create")

	// delete the input buffer
	runTygerSucceeds(t, "buffer", "delete", inputBufferId)

	args := []string{"run", "create", "--codespec", codespecName,
		"--buffer", fmt.Sprintf("input=%s", inputBufferId),
		"--buffer", fmt.Sprintf("output=%s", outputBufferId)}

	// attempt to create run
	_, stderr, err := runTyger(args...)
	require.Error(err)
	require.Contains(stderr, fmt.Sprintf("The buffer '%s' was not found", inputBufferId))

	// restore the input buffer
	runTygerSucceeds(t, "buffer", "restore", inputBufferId)
	// delete the output buffer
	runTygerSucceeds(t, "buffer", "delete", outputBufferId)

	// attempt to create run
	_, stderr, err = runTyger(args...)
	require.Error(err)
	require.Contains(stderr, fmt.Sprintf("The buffer '%s' was not found", outputBufferId))
}

func TestImagePull(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	runSpec := `
job:
  codespec:
    image: mcr.microsoft.com/cbl-mariner/busybox:1.35
    command:
      - "sh"
      - "-c"
      - |
        echo "hello"
  tags:
    testName: TestEndToEndWithYamlSpecAndAutomaticallyCreatedBuffers
timeoutSeconds: 600`

	tempDir := t.TempDir()
	runSpecPath := filepath.Join(tempDir, "runspec.yaml")
	require.NoError(os.WriteFile(runSpecPath, []byte(runSpec), 0644))

	// create run
	runTygerSucceeds(t, "run", "exec", "--file", runSpecPath, "--pull")
}

func TestWorkloadIdentity(t *testing.T) {
	t.Parallel()
	skipIfUsingUnixSocket(t)

	require := require.New(t)

	runSpec := fmt.Sprintf(`
job:
  codespec:
    image: %s
    identity: test-identity
    command:
      - "sh"
      - "-c"
      - |
        set -euo pipefail
        az login --federated-token "$(cat $AZURE_FEDERATED_TOKEN_FILE)" --service-principal -u $AZURE_CLIENT_ID -t $AZURE_TENANT_ID --allow-no-subscriptions
        az account get-access-token > /dev/null
timeoutSeconds: 600`, AzCliImage)

	tempDir := t.TempDir()
	runSpecPath := filepath.Join(tempDir, "runspec.yaml")
	require.NoError(os.WriteFile(runSpecPath, []byte(runSpec), 0644))

	runTygerSucceeds(t, "run", "exec", "--file", runSpecPath, "--logs")
}

func TestMissingWorkloadIdentity(t *testing.T) {
	t.Parallel()
	skipIfUsingUnixSocket(t)

	require := require.New(t)

	runSpec := fmt.Sprintf(`
job:
  codespec:
    image: %s
    command:
      - "sh"
      - "-c"
      - |
        set -euo pipefail
        az login --federated-token "$(cat $AZURE_FEDERATED_TOKEN_FILE)" --service-principal -u $AZURE_CLIENT_ID -t $AZURE_TENANT_ID --allow-no-subscriptions
        az account get-access-token > /dev/null
timeoutSeconds: 600`, AzCliImage)

	tempDir := t.TempDir()
	runSpecPath := filepath.Join(tempDir, "runspec.yaml")
	require.NoError(os.WriteFile(runSpecPath, []byte(runSpec), 0644))

	_, _, err := runTyger("run", "exec", "--file", runSpecPath, "--logs")
	assert.Error(t, err)
}

func TestWorkloadIdentityWithInvalidIdentity(t *testing.T) {
	t.Parallel()
	skipIfUsingUnixSocket(t)

	require := require.New(t)

	runSpec := fmt.Sprintf(`
job:
  codespec:
    image: %s
    identity: invalid-identity
    command: date
timeoutSeconds: 600`, BasicImage)

	tempDir := t.TempDir()
	runSpecPath := filepath.Join(tempDir, "runspec.yaml")
	require.NoError(os.WriteFile(runSpecPath, []byte(runSpec), 0644))

	_, _, err := runTyger("run", "exec", "--file", runSpecPath, "--logs")
	assert.Error(t, err)
}

func TestExport(t *testing.T) {
	t.Parallel()
	skipIfOnlyFastTests(t)
	skipIfUsingUnixSocket(t)

	saString := runTygerSucceeds(t, "buffer", "storage-account", "list")
	var saList []model.StorageAccount
	require.NoError(t, json.Unmarshal([]byte(saString), &saList))

	location1 := saList[0].Location
	location1AccountNames := []string{}
	for _, sa := range saList {
		if sa.Location == location1 {
			location1AccountNames = append(location1AccountNames, sa.Name)
		}
	}

	var location2AccountName string
	var location2Endpoint string
	var location2 string
	for _, sa := range saList {
		if sa.Location != location1 {
			location2 = sa.Location
			location2AccountName = sa.Name
			location2Endpoint = sa.Endpoint
			break
		}
	}

	if location2 == "" {
		t.Skip("Skipping test because there is no second storage account region")
	}

	require.NotEmpty(t, location1)

	testId := uuid.NewString()

	originalBufferIds := []string{}
	for i := range 20 {
		var location string
		if i%4 == 0 {
			location = location1
		} else {
			location = location2
		}

		id := runTygerSucceeds(t, "buffer", "create", "--location", location, "--tag", fmt.Sprintf("exporttestindex=%d", i), "--tag", fmt.Sprintf("exporttest=%s", testId))
		originalBufferIds = append(originalBufferIds, id)

		writeCommand := exec.Command("tyger", "buffer", "write", id)
		writeCommand.Stdin = bytes.NewBufferString("hello")

		writeStdErr := &bytes.Buffer{}
		writeCommand.Stderr = writeStdErr

		assert.NoError(t, writeCommand.Run())
	}

	for _, sourceAccountName := range location1AccountNames {
		runTygerSucceeds(t, "buffer", "export", location2Endpoint, "--source-storage-account", sourceAccountName, "--tag", fmt.Sprintf("exporttest=%s", testId), "--hash-ids")
	}

	runTygerSucceeds(t, "buffer", "import", "--storage-account", location2AccountName)

	buffers := listBuffers(t, "--tag", fmt.Sprintf("exporttest=%s", testId))
	assert.Len(t, buffers, len(originalBufferIds)*5/4)
	for _, buffer := range buffers {
		assert.Len(t, buffer.Tags, 2)
	}
}

func TestTagRun(t *testing.T) {
	t.Parallel()

	codespecName := strings.ToLower(t.Name())

	runTygerSucceeds(t,
		"codespec",
		"create", codespecName,
		"--image", BasicImage,
		"--command",
		"--",
		"echo", "hi")

	runId := runTygerSucceeds(t, "run", "create", "--codespec", codespecName, "--timeout", "10m", "--tag", "a=1")
	waitForRunSuccess(t, runId)

	run := getRun(t, runId)
	require.Len(t, run.Tags, 1)

	var err error
	run, err = setRun(t, runId, "--tag", "b=2")
	require.NoError(t, err)
	require.Len(t, run.Tags, 2)

	run, err = setRun(t, runId, "--clear-tags", "--tag", "c=3")
	require.NoError(t, err)
	require.Len(t, run.Tags, 1)
	require.Equal(t, "3", run.Tags["c"])

	run, err = setRun(t, runId, "--tag", "d=4", "--etag", run.ETag)
	require.NoError(t, err)
	require.Len(t, run.Tags, 2)

	run, err = setRun(t, runId, "--tag", "e=5", "--etag", "999999")
	require.ErrorContains(t, err, "the server's ETag does not match the provided ETag")

	run, err = setRun(t, runId, "--tag", "e=5", "--etag", "banana")
	require.ErrorContains(t, err, "the server's ETag does not match the provided ETag")
}

func TestRunETagChanges(t *testing.T) {
	hangingCodespecName := strings.ToLower(t.Name())
	runTygerSucceeds(t,
		"codespec",
		"create", hangingCodespecName,
		"--image", BasicImage,
		"--command",
		"--",
		"sleep", "60")

	runTygerSucceeds(t, "run", "create", "--codespec", hangingCodespecName, "--timeout", "10m")
	runId := runTygerSucceeds(t, "run", "create", "--codespec", hangingCodespecName, "--timeout", "10m")

	run := waitForRunStarted(t, runId)
	etag := run.ETag

	var err error
	run, err = setRun(t, runId, "--tag", "a=1")
	require.NoError(t, err)
	require.NotEqual(t, etag, run.ETag)
	etag = run.ETag

	runTygerSucceeds(t, "run", "cancel", runId)

	run = getRun(t, runId)
	require.NotEqual(t, etag, run.ETag)
}

func TestServerLogs(t *testing.T) {
	t.Parallel()

	dockerParam := ""
	if isUsingUnixSocketDirectlyOrIndirectly() {
		dockerParam = "--docker"
	}

	logs := runCommandSucceeds(t, "bash", "-c", fmt.Sprintf("tyger api logs --tail 1 -f <(../../scripts/get-config.sh %s) --org lamna", dockerParam))
	lines := strings.Split(logs, "\n")
	require.Equal(t, 1, len(lines))
	require.Contains(t, lines[0], `"timestamp"`)
}

func TestServerApiVersioningErrors(t *testing.T) {
	t.Parallel()

	bufferId := runTygerSucceeds(t, "buffer", "create")

	errorInfo := &model.ErrorInfo{}

	resp, err := controlplane.InvokeRequest(context.Background(), http.MethodGet, fmt.Sprintf("/buffers/%s?api-version=0.1", bufferId), nil, nil, nil)
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, err.Error(), "Unsupported API version")
	require.ErrorAs(t, err, &errorInfo)
	require.Contains(t, errorInfo.ApiVersions, "1.0")

	resp, err = controlplane.InvokeRequest(context.Background(), http.MethodGet, fmt.Sprintf("/buffers/%s?api-version=", bufferId), nil, nil, nil)
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, err.Error(), "Unspecified API version")
	require.ErrorAs(t, err, &errorInfo)
	require.Contains(t, errorInfo.ApiVersions, "1.0")

	badApiVersionCtx := controlplane.SetApiVersionOnContext(context.Background(), "0.1")
	resp, err = controlplane.InvokeRequest(badApiVersionCtx, http.MethodGet, fmt.Sprintf("/buffers/%s", bufferId), nil, nil, nil)
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, err.Error(), "Unsupported API version")
	require.ErrorAs(t, err, &errorInfo)
	require.Contains(t, errorInfo.ApiVersions, "1.0")

	missingApiVersionCtx := controlplane.SetApiVersionOnContext(context.Background(), "")
	resp, err = controlplane.InvokeRequest(missingApiVersionCtx, http.MethodGet, fmt.Sprintf("/buffers/%s", bufferId), nil, nil, nil)
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Contains(t, err.Error(), "Unspecified API version")
	require.ErrorAs(t, err, &errorInfo)
	require.Contains(t, errorInfo.ApiVersions, "1.0")

	// Anonymous endpoints, like /metadata, should ignore API version
	var anonymousEndpoints = []string{
		"/metadata",
		"/healthcheck",
	}
	for _, endpoint := range anonymousEndpoints {
		resp, err := controlplane.InvokeRequest(context.Background(), http.MethodGet, fmt.Sprintf("%s?api-version=0.1", endpoint), nil, nil, nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		resp, err = controlplane.InvokeRequest(context.Background(), http.MethodGet, fmt.Sprintf("%s?api-version=", endpoint), nil, nil, nil)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
	}
}

func TestServerApiV1BackwardCompatibility(t *testing.T) {
	t.Parallel()

	metadata := model.ServiceMetadata{}
	_, err := controlplane.InvokeRequest(context.Background(), http.MethodGet, "v1/metadata", nil, nil, &metadata)
	require.NoError(t, err)
	_, err = controlplane.InvokeRequest(context.Background(), http.MethodGet, "v1/metadata?api-version=", nil, nil, &metadata)
	require.NoError(t, err)
	_, err = controlplane.InvokeRequest(context.Background(), http.MethodGet, "v1/metadata?api-version=0.1", nil, nil, nil)
	require.NoError(t, err)

	buffer := model.Buffer{}
	newBuffer := model.Buffer{}
	_, err = controlplane.InvokeRequest(context.Background(), http.MethodPost, "/v1/buffers", nil, buffer, &newBuffer)
	require.NoError(t, err)
	_, err = controlplane.InvokeRequest(context.Background(), http.MethodPost, "/v1/buffers?api-version=", nil, buffer, &newBuffer)
	require.NoError(t, err)
	_, err = controlplane.InvokeRequest(context.Background(), http.MethodPost, "/v1/buffers?api-version=0.1", nil, buffer, nil)
	require.Error(t, err)

	_, err = controlplane.InvokeRequest(context.Background(), http.MethodGet, fmt.Sprintf("v1/buffers/%s", newBuffer.Id), nil, nil, &buffer)
	require.NoError(t, err)
	require.Equal(t, newBuffer.Id, buffer.Id)

	count := 0
	for url := "/v1/codespecs?limit=5"; url != ""; {
		page := model.Page[model.Codespec]{}
		_, err := controlplane.InvokeRequest(context.Background(), http.MethodGet, url, nil, nil, &page)
		require.Nil(t, err)
		count += len(page.Items)
		if page.NextLink == "" || count > 50 {
			break
		}
		url = page.NextLink
	}

	_, err = controlplane.InvokeRequest(context.Background(), http.MethodPost, "v1/runs/_sweep", nil, nil, nil)
	require.Nil(t, err)

	count = 0
	for url := "/v1/runs?limit=5"; url != ""; {
		page := model.Page[model.Run]{}
		_, err := controlplane.InvokeRequest(context.Background(), http.MethodGet, url, nil, nil, &page)
		require.Nil(t, err)
		count += len(page.Items)
		if page.NextLink == "" || count > 50 {
			break
		}
		url = page.NextLink
	}
}

func TestBufferAccessUrlUpdates(t *testing.T) {
	t.Parallel()

	bufferId := runTygerSucceeds(t, "buffer", "create")

	ttl, err := common.ParseTimeToLive("0.00:00:30")
	require.NoError(t, err)

	t.Run("from buffer id", func(t *testing.T) {
		t.Parallel()
		require := require.New(t)
		container, err := dataplane.NewContainerFromBufferId(context.Background(), bufferId, true, ttl.String())
		require.NoError(err)
		firstAccessUrl, err := container.GetValidAccessUrl(context.Background())
		require.NoError(err)
		time.Sleep(31 * time.Second)
		nextAccessUrl, err := container.GetValidAccessUrl(context.Background())
		require.NoError(err)
		require.NotEqual(firstAccessUrl.String(), nextAccessUrl.String())
	})

	t.Run("from filename", func(t *testing.T) {
		t.Parallel()
		require := require.New(t)

		accessUrl := runTygerSucceeds(t, "buffer", "access", bufferId, "--access-ttl", ttl.String())

		tempDir := t.TempDir()
		accessFilePath := filepath.Join(tempDir, fmt.Sprintf("%s.access", bufferId))
		require.NoError(os.WriteFile(accessFilePath, []byte(accessUrl), 0644))
		container, err := dataplane.NewContainerFromAccessFile(context.Background(), accessFilePath)
		require.NoError(err)
		accessUrlRead, err := container.GetValidAccessUrl(context.Background())
		require.NoError(err)
		require.Equal(accessUrl, accessUrlRead.String())

		time.Sleep(10 * time.Second)
		newAccessUrl := runTygerSucceeds(t, "buffer", "access", bufferId, "--access-ttl", ttl.String())
		require.NotEqual(accessUrl, newAccessUrl)
		require.NoError(os.WriteFile(accessFilePath, []byte(newAccessUrl), 0644))
		time.Sleep(21 * time.Second)
		accessUrlRead, err = container.GetValidAccessUrl(context.Background())
		require.NoError(err)
		require.Equal(newAccessUrl, accessUrlRead.String())
	})

	t.Run("from buffer access url", func(t *testing.T) {
		t.Parallel()
		require := require.New(t)
		accessUrl := runTygerSucceeds(t, "buffer", "access", bufferId, "--access-ttl", ttl.String())
		container, err := dataplane.NewContainerFromAccessString(context.Background(), accessUrl)
		require.NoError(err)
		firstAccessUrl, err := container.GetValidAccessUrl(context.Background())
		require.NoError(err)
		require.Equal(accessUrl, firstAccessUrl.String())
		time.Sleep(31 * time.Second)
		// URL will not be refreshed because the input is a SAS URL (the user may not be logged into Tyger)
		nextAccessUrl, err := container.GetValidAccessUrl(context.Background())
		require.Nil(nextAccessUrl)
		require.Error(err)
		require.ErrorContains(err, "access URL expired and cannot be refreshed")
	})
}

func TestServiceMetadataContainsApiVersions(t *testing.T) {
	t.Parallel()

	metadata := getServiceMetadata(t)
	require.NotEmpty(t, metadata.ApiVersions)
	require.Contains(t, metadata.ApiVersions, "1.0")
}

func waitForRunStarted(t *testing.T, runId string) model.Run {
	t.Helper()
	return waitForRun(t, runId, true, false)
}

func waitForRunSuccess(t *testing.T, runId string) model.Run {
	t.Helper()
	return waitForRun(t, runId, false, false)
}

func waitForRunCanceled(t *testing.T, runId string) model.Run {
	t.Helper()
	return waitForRun(t, runId, false, true)
}

func waitForRun(t *testing.T, runId string, returnOnRunning bool, returnOnCancel bool) model.Run {
	t.Helper()

start:
	cmd := exec.Command("tyger", "run", "watch", runId, "--full-resource")

	stdout, err := cmd.StdoutPipe()
	stdoutScanner := bufio.NewScanner(stdout)
	require.NoError(t, err, "unable to get stdout pipe for tyger run watch")

	var errb bytes.Buffer
	cmd.Stderr = &errb

	require.NoError(t, cmd.Start(), "unable to start tyger run watch")
	defer cmd.Process.Kill()

	snapshot := model.Run{}
	for {
		if !stdoutScanner.Scan() {
			require.NoError(t, stdoutScanner.Err(), "error reading stdout from tyger run watch")
			// The stream ended before we reached the status we were expecting.
			// Restart the watch
			goto start
		}
		line := stdoutScanner.Text()
		require.NoError(t, json.Unmarshal([]byte(line), &snapshot))

		require.NotNil(t, snapshot.Status, "run '%d' status was nil", snapshot.Id)

		switch *snapshot.Status {
		case model.Pending:
		case model.Running:
			if returnOnRunning {
				return snapshot
			}
		case model.Succeeded:
			goto done
		case model.Canceling:
		case model.Canceled:
			if returnOnCancel {
				return snapshot
			}
			require.FailNowf(t, "run was canceled.", "Run '%d'. Last status: %s", snapshot.Id, *snapshot.Status)
		case model.Failed:
			statusString := fmt.Sprintf("%s", *snapshot.Status)
			if snapshot.StatusReason != "" {
				statusString = fmt.Sprintf("%s (%s)", statusString, snapshot.StatusReason)
			}

			stdOut, stdErr, err := runTyger("run", "logs", fmt.Sprintf("%d", snapshot.Id))
			if err == nil {
				t.Log(fmt.Sprintf("Run %d logs:\n", snapshot.Id), stdOut, stdErr)
			}

			require.FailNowf(t, "run failed.", "Run '%d'. Last status: %s", snapshot.Id, statusString)
		default:
			require.FailNowf(t, "unexpected run status.", "Run '%d'. Last status: %s", snapshot.Id, *snapshot.Status)
		}
	}

done:
	err = cmd.Wait()
	require.NoError(t, err, "tyger run watch failed: %s", errb.String())
	return snapshot
}

func getTestConnectivityImage(t *testing.T) string {
	t.Helper()

	if imgVar := os.Getenv("TEST_CONNECTIVITY_IMAGE"); imgVar != "" {
		return imgVar
	}

	devConfig := getDevConfig(t)
	containerRegistryFqdn := devConfig["wipContainerRegistry"].(map[string]any)["fqdn"].(string)

	c, err := controlplane.GetClientFromCache()
	require.NoError(t, err)
	var tag string
	var format string
	switch c.ControlPlaneUrl.Scheme {
	case "http+unix", "https+unix":
		tag = "dev-" + runtime.GOARCH
		format = "{{ .Id }}"
	default:
		tag = "dev-amd64"
		format = "{{ index .RepoDigests 0 }}"
	}

	image := fmt.Sprintf("%s/testconnectivity:%s", containerRegistryFqdn, tag)

	return runCommandSucceeds(t, "docker", "inspect", image, "--format", format)
}
