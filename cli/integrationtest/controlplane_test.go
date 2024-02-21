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
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/andreyvit/diff"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/google/uuid"
	"github.com/hashicorp/go-retryablehttp"
	"github.com/microsoft/tyger/cli/internal/controlplane"
	"github.com/microsoft/tyger/cli/internal/controlplane/model"
	"github.com/microsoft/tyger/cli/internal/httpclient"
	"github.com/microsoft/tyger/cli/internal/settings"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/yaml"
)

func init() {
	stdout, stderr, err := runTyger("login", "status")
	if err != nil {
		fmt.Fprintln(os.Stderr, stderr, stdout)
		log.Fatal().Err(err).Send()
	}

	log.Logger = log.Logger.Level(zerolog.ErrorLevel)
}

const (
	BasicImage = "mcr.microsoft.com/cbl-mariner/base/core:2.0"
)

func getServiceMetadata(t *testing.T) model.ServiceMetadata {
	t.Helper()
	ctx, _ := getServiceInfoContext(t)
	metadata := model.ServiceMetadata{}
	_, err := controlplane.InvokeRequest(ctx, http.MethodGet, "v1/metadata", nil, &metadata)
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

func supportsMultipleNodePools(t *testing.T) bool {
	t.Helper()
	return hasCapability(t, "NodePools")
}

func supportsDistributedRuns(t *testing.T) bool {
	t.Helper()
	return hasCapability(t, "DistributedRuns")
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
	inputSasUri := runTygerSucceeds(t, "buffer", "access", inputBufferId, "-w")

	// create and output buffer and a SAS token to be able to read from it
	outputBufferId := runTygerSucceeds(t, "buffer", "create")
	outputSasUri := runTygerSucceeds(t, "buffer", "access", outputBufferId)

	runCommandSucceeds(t, "sh", "-c", fmt.Sprintf(`echo "Hello" | tyger buffer write "%s"`, inputSasUri))

	// create run
	runId := runTygerSucceeds(t, "run", "create", "--codespec", codespecName, "--timeout", "10m",
		"-b", fmt.Sprintf("input=%s", inputBufferId),
		"-b", fmt.Sprintf("output=%s", outputBufferId))

	waitForRunSuccess(t, runId)

	output := runCommandSucceeds(t, "sh", "-c", fmt.Sprintf(`tyger buffer read "%s"`, outputSasUri))

	require.Equal("Hello: Bonjour", output)
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

	runJson := runTygerSucceeds(t, "run", "show", runId)

	var run model.Run
	require.NoError(json.Unmarshal([]byte(runJson), &run))

	inputBufferId := run.Job.Buffers["input"]
	outputBufferId := run.Job.Buffers["output"]

	runCommandSucceeds(t, "sh", "-c", fmt.Sprintf(`echo "Hello" | tyger buffer write "%s"`, inputBufferId))

	waitForRunSuccess(t, runId)

	output := runCommandSucceeds(t, "sh", "-c", fmt.Sprintf(`tyger buffer read "%s"`, outputBufferId))

	require.Equal("Hello: Bonjour", output)
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

	runJson := runTygerSucceeds(t, "run", "show", runId)

	var run model.Run
	require.NoError(json.Unmarshal([]byte(runJson), &run))

	inputBufferId := run.Job.Buffers["input"]
	inputSasUri := runTygerSucceeds(t, "buffer", "access", inputBufferId, "-w")
	outputBufferId := run.Job.Buffers["output"]
	outputSasUri := runTygerSucceeds(t, "buffer", "access", outputBufferId)

	runCommandSucceeds(t, "sh", "-c", fmt.Sprintf(`echo "Hello" | tyger buffer write "%s"`, inputSasUri))

	waitForRunSuccess(t, runId)

	output := runCommandSucceeds(t, "sh", "-c", fmt.Sprintf(`tyger buffer read "%s"`, outputSasUri))

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

	execStdOut := NewTygerCmdBuilder("run", "exec", "--file", runSpecPath, "--tag", "testName=TestCodespecBufferTagsWithYamlSpec", "--tag", "testtagX="+uniqueId, "--log-level", "trace").
		Stdin("Hello").
		RunSucceeds(t)

	require.Equal("Hello: Bonjour", execStdOut)

	bufferJson := runTygerSucceeds(t, "buffer", "list", "--tag", "testName=TestCodespecBufferTagsWithYamlSpec", "--tag", "testtagX="+uniqueId)

	var buffers []model.Buffer
	require.NoError(json.Unmarshal([]byte(bufferJson), &buffers))

	require.Equal(2, len(buffers))
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

func mustParseQuentity(s string) *resource.Quantity {
	q, err := resource.ParseQuantity(s)
	if err != nil {
		panic(err)
	}
	return &q
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

	require.Equal(t, codespecName, receivedSpec.Name)
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

	require.Equal(t, codespec2Name, receivedSpec.Name)

	// now override the spec name and image
	codespec3Name := strings.ToLower(t.Name() + "3")
	runTygerSucceeds(t, "codespec", "create", codespec3Name, "-f", specPath, "--image", "ubuntu")

	receivedSpecString = runTygerSucceeds(t, "codespec", "show", codespec3Name)
	require.NoError(t, json.Unmarshal([]byte(receivedSpecString), &receivedSpec))

	require.Equal(t, codespec3Name, receivedSpec.Name)
	require.Equal(t, "ubuntu", receivedSpec.Image)
}
func TestInvalidCodespecNames(t *testing.T) {
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
			ctx, _ := getServiceInfoContext(t)
			_, err = controlplane.InvokeRequest(ctx, http.MethodPut, fmt.Sprintf("v1/codespecs/%s", tC.name), newCodespec, nil)
			if tC.valid {
				require.Nil(t, err)
			} else {
				require.NotNil(t, err)
			}
		})
	}
}

func TestCodespecNameRequirements(t *testing.T) {
	runTyger("codespec", "create", "Foo", "--image", BasicImage)
}

// Verify that a run using a codespec that requires a GPU
// is scheduled on a node with one.
func TestGpuResourceRequirement(t *testing.T) {
	t.Parallel()

	const codespecName = "gputestcodespec"
	runTygerSucceeds(t,
		"codespec",
		"create", codespecName,
		"--image", "nvidia/cuda:11.0.3-base-ubuntu20.04",
		"--gpu", "1",
		"--command",
		"--",
		"bash", "-c", "[[ $(nvidia-smi -L | wc -l) == 1 ]]") // verify that a GPU is available

	// create run
	runId := runTygerSucceeds(t, "run", "create", "--codespec", codespecName, "--timeout", "10m")

	run := waitForRunSuccess(t, runId)

	require.NoError(t, json.Unmarshal([]byte(runTygerSucceeds(t, "run", "show", runId)), &run))
	if supportsMultipleNodePools(t) {
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
		"--image", "nvidia/cuda:11.0.3-base-ubuntu20.04",
		"--command",
		"--",
		"bash", "-c", "[[ ! $(nvidia-smi) ]]") // verify that no GPU is available

	// create run
	runId := runTygerSucceeds(t, "run", "create", "--codespec", codespecName, "--timeout", "10m")

	waitForRunSuccess(t, runId)
}

func TestTargetGpuNodePool(t *testing.T) {
	t.Parallel()
	if !supportsMultipleNodePools(t) {
		t.Skip("NodePools capability not supported")
	}

	codespecName := strings.ToLower(t.Name())
	runTygerSucceeds(t,
		"codespec",
		"create", codespecName,
		"--image", "nvidia/cuda:11.0.3-base-ubuntu20.04",
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
		"--image", "nvidia/cuda:11.0.3-base-ubuntu20.04",
		"--command",
		"--",
		"bash", "-c", "[[ ! $(nvidia-smi) ]]") // verify that no GPU is available

	// create run
	runId := runTygerSucceeds(t, "run", "create", "--codespec", codespecName, "--node-pool", "cpunp", "--timeout", "10m")

	waitForRunSuccess(t, runId)
}

func TestTargetingInvalidClusterReturnsError(t *testing.T) {
	t.Parallel()
	if !supportsMultipleNodePools(t) {
		t.Skip("NodePools capability not supported")
	}

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
	if !supportsMultipleNodePools(t) {
		t.Skip("NodePools capability not supported")
	}

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
	if !supportsMultipleNodePools(t) {
		t.Skip("NodePools capability not supported")
	}

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
	ctx, _ := getServiceInfoContext(t)

	codespec := model.Codespec{}
	requestBody := map[string]string{"kind": "job", "image": "x"}
	_, err := controlplane.InvokeRequest(ctx, http.MethodPut, "v1/codespecs/tcs", requestBody, &codespec)
	require.Nil(err)

	requestBody["unknownField"] = "y"
	_, err = controlplane.InvokeRequest(ctx, http.MethodPut, "v1/codespecs/tcs", requestBody, &codespec)
	require.NotNil(err)
}

func TestInvalidCodespecDiscriminatorRejected(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	ctx, _ := getServiceInfoContext(t)

	codespec := model.Codespec{}
	requestBody := map[string]string{"image": "x"}
	_, err := controlplane.InvokeRequest(ctx, http.MethodPut, "v1/codespecs/tcs", requestBody, &codespec)
	require.ErrorContains(err, "Missing discriminator property 'kind'")

	requestBody["kind"] = "missing"
	_, err = controlplane.InvokeRequest(ctx, http.MethodPut, "v1/codespecs/tcs", requestBody, &codespec)
	require.ErrorContains(err, "Invalid value for the property 'kind'. It can be either 'job' or 'worker'")
}

func TestInvalidCodespecMissingRequiredFieldsRejected(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	ctx, _ := getServiceInfoContext(t)

	codespec := model.Codespec{}
	requestBody := map[string]string{"kind": "job"}
	_, err := controlplane.InvokeRequest(ctx, http.MethodPut, "v1/codespecs/tcs", requestBody, &codespec)
	require.ErrorContains(err, "missing required properties, including the following: image")
}

func TestResponseContainsRequestIdHeader(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	_, stderr, _ := runTyger("codespec", "show", "missing")

	require.Contains(stderr, "Request-Id")
}

func TestOpenApiSpecIsAsExpected(t *testing.T) {
	t.Parallel()
	ctx, serviceInfo := getServiceInfoContext(t)

	swaggerUri := fmt.Sprintf("%s/swagger/v1/swagger.yaml", serviceInfo.GetServerUri())
	req, err := retryablehttp.NewRequestWithContext(ctx, http.MethodGet, swaggerUri, nil)
	require.NoError(t, err)
	resp, err := httpclient.DefaultRetryableClient.Do(req)
	require.Nil(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	actualBytes, err := io.ReadAll(resp.Body)
	require.Nil(t, err)
	expectedFilePath, err := filepath.Abs("expected_openapi_spec.yaml")
	require.Nil(t, err)
	expectedBytes, err := os.ReadFile(expectedFilePath)
	require.Nil(t, err)

	if a, e := strings.TrimSpace(string(actualBytes)), strings.TrimSpace(string(expectedBytes)); a != e {
		t.Errorf("Result not as expected. To update, run `curl %s > %s`\n\nDiff:%v",
			swaggerUri,
			expectedFilePath,
			diff.LineDiff(e, a))
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
	ctx, _ := getServiceInfoContext(t)

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

	for uri := "v1/runs?limit=5"; uri != ""; {
		page := model.Page[model.Run]{}
		_, err := controlplane.InvokeRequest(ctx, http.MethodGet, uri, nil, &page)
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

		uri = strings.TrimLeft(page.NextLink, "/")
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
		if _, ok := codespecMap[cs.Name]; ok {
			require.Equal(t, codespecNames[csIdx], cs.Name)
			require.Equal(t, codespecMap[cs.Name], strconv.Itoa(cs.Version))
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
	ctx, _ := getServiceInfoContext(t)

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

	for uri := fmt.Sprintf("v1/codespecs?limit=5&prefix=%s", prefix); uri != ""; {
		page := model.Page[model.Codespec]{}
		_, err := controlplane.InvokeRequest(ctx, http.MethodGet, uri, nil, &page)
		require.Nil(t, err)
		for _, cs := range page.Items {
			if _, ok := codespecs[cs.Name]; ok {
				if expectedIdx < 5 {
					returnedNames1[expectedIdx] = cs.Name
					expectedIdx++
					if cs.Name == prefix+"klamath" {
						currentKlamathVersion = cs.Version
					}
				} else {
					returnedNames2[expectedIdx-5] = cs.Name
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

		uri = strings.TrimLeft(page.NextLink, "/")
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

	midRunJson := runTygerSucceeds(t, "run", "show", midId)
	midRun := model.Run{}
	err := json.Unmarshal([]byte(midRunJson), &midRun)
	require.Nil(t, err)

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

func TestListCodespecsWithPrefix(t *testing.T) {
	t.Parallel()
	ctx, _ := getServiceInfoContext(t)

	codespecNames := [4]string{"3d_t2_flair", "t1w-1mm-ax", "t1w-0.9mm-sag", "3d_t1_star"}
	codespecMap := make(map[string]string)
	for i := 0; i < 4; i++ {
		codespecMap[codespecNames[i]] = runTygerSucceeds(t, "codespec", "create", codespecNames[i], "--image", BasicImage)
	}

	uri := "v1/codespecs?prefix=3d_"
	page := model.Page[model.Codespec]{}
	_, err := controlplane.InvokeRequest(ctx, http.MethodGet, uri, nil, &page)
	require.Nil(t, err)
	for _, cs := range page.Items {
		require.Equal(t, strings.HasPrefix(cs.Name, "3d_"), true)
		delete(codespecMap, cs.Name)
	}
	require.Equal(t, len(codespecMap), 2)

	for cs := range codespecMap {
		require.Equal(t, strings.HasPrefix(cs, "t1w-"), true)
	}
}

func TestGetLogsFromPod(t *testing.T) {
	t.Parallel()
	ctx, _ := getServiceInfoContext(t)

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
	resp, err := controlplane.InvokeRequest(ctx, http.MethodGet, fmt.Sprintf("v1/runs/%s/logs?follow=true", runId), nil, nil)
	require.Nil(t, err)
	reader := bufio.NewReader(resp.Body)
	for i := 0; i < 5; i++ {
		_, err = reader.ReadString('\n')
		require.Nil(t, err)
	}

	require.Nil(t, resp.Body.Close())

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
	ctx, _ := getServiceInfoContext(t)

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
	_, err := controlplane.InvokeRequest(ctx, http.MethodPost, "v1/runs/_sweep", nil, nil)
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
	ctx, _ := getServiceInfoContext(t)

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
	_, err := controlplane.InvokeRequest(ctx, http.MethodPost, "v1/runs/_sweep", nil, nil)
	require.Nil(t, err)

	logs = runTygerSucceeds(t, "run", "logs", runId)
	require.Equal(t, expectedLogs, logs)
}

func TestConnectivityBetweenJobAndWorkers(t *testing.T) {
	t.Parallel()
	if !supportsDistributedRuns(t) {
		t.Skip("Distributed runs not supported")
	}

	jobCodespecName := strings.ToLower(t.Name()) + "-job"
	workerCodespecName := strings.ToLower(t.Name()) + "-worker"

	digest := getTestConnectivityImage(t)

	runTygerSucceeds(t,
		"codespec",
		"create", jobCodespecName,
		"--image", digest,
		"--",
		"--job")

	runTygerSucceeds(t,
		"codespec",
		"create", workerCodespecName,
		"--kind", "worker",
		"--image", digest,
		"--max-replicas", "3",
		"--endpoint", "TestWorker=29477",
		"--",
		"--worker")

	runId := runTygerSucceeds(t, "run", "create", "--codespec", jobCodespecName, "--worker-codespec", workerCodespecName, "--worker-replicas", "3", "--timeout", "10m")
	waitForRunSuccess(t, runId)
}

func TestAuthenticationRequired(t *testing.T) {
	t.Parallel()
	if getServiceMetadata(t).Authority == "" {
		t.Skip("Authentication disabled for this server")
	}

	ctx, serviceInfo := getServiceInfoContext(t)
	req, err := retryablehttp.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/v1/runs/abc", serviceInfo.GetServerUri()), nil)
	require.NoError(t, err)
	resp, err := httpclient.DefaultRetryableClient.Do(req)
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

func TestCancelJob(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	ctx, _ := getServiceInfoContext(t)

	codespecName := strings.ToLower(t.Name())

	runTygerSucceeds(t,
		"codespec",
		"create", codespecName,
		"--image", getTestConnectivityImage(t),
		"--",
		"--worker")

	runId := runTygerSucceeds(t, "run", "create", "--codespec", codespecName, "--timeout", "10m")
	t.Logf("Run ID: %s", runId)

	runTygerSucceeds(t, "run", "cancel", runId)

	// force the sweep to run to terminate the pod
	_, err := controlplane.InvokeRequest(ctx, http.MethodPost, "v1/runs/_sweep", nil, nil)
	require.NoError(err)

	waitForRunCanceled(t, runId)

	// Check that the run failed because it was canceled.
	runJson := runTygerSucceeds(t, "run", "show", runId)

	var run model.Run
	require.NoError(json.Unmarshal([]byte(runJson), &run))

	require.Equal(model.Canceled, *run.Status)
	require.Equal("Canceled by user", run.StatusReason)
}

func TestBufferWithoutTags(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	bufferId := runTygerSucceeds(t, "buffer", "create")
	t.Logf("Buffer ID: %s", bufferId)

	bufferJson := runTygerSucceeds(t, "buffer", "show", bufferId)

	var buffer model.Buffer
	require.NoError(json.Unmarshal([]byte(bufferJson), &buffer))

	require.Equal(0, len(buffer.Tags))
}

func TestBufferWithTags(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	bufferId := runTygerSucceeds(t, "buffer", "create", "--tag", "testtag1=testvalue1", "--tag", "testtag2=testvalue2")
	t.Logf("Buffer ID: %s", bufferId)

	bufferJson := runTygerSucceeds(t, "buffer", "show", bufferId)

	var buffer model.Buffer
	require.NoError(json.Unmarshal([]byte(bufferJson), &buffer))

	require.Equal(2, len(buffer.Tags))
	require.Equal("testvalue1", buffer.Tags["testtag1"])
	require.Equal("testvalue2", buffer.Tags["testtag2"])
}

func TestBufferSetTags(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	bufferId := runTygerSucceeds(t, "buffer", "create", "--tag", "testtag1=testvalue1", "--tag", "testtag2=testvalue2")
	t.Logf("Buffer ID: %s", bufferId)

	bufferJson := runTygerSucceeds(t, "buffer", "set", bufferId, "--tag", "testtag3=testvalue3", "--tag", "testtag4=testvalue4")

	var buffer model.Buffer
	require.NoError(json.Unmarshal([]byte(bufferJson), &buffer))

	require.Equal(2, len(buffer.Tags))
	require.Equal("testvalue3", buffer.Tags["testtag3"])
	require.Equal("testvalue4", buffer.Tags["testtag4"])
}

func TestBufferSetTagsWithETag(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	bufferJson := runTygerSucceeds(t, "buffer", "create", "--tag", "testtag1=testvalue1", "--tag", "testtag2=testvalue2", "--full-resource")

	var bufferETag model.Buffer
	require.NoError(json.Unmarshal([]byte(bufferJson), &bufferETag))

	t.Logf("Buffer ID: %s eTag: %s", bufferETag.Id, bufferETag.ETag)

	bufferJson = runTygerSucceeds(t, "buffer", "set", bufferETag.Id, "--tag", "testtag3=testvalue3", "--tag", "testtag4=testvalue4", "--etag", bufferETag.ETag)

	var buffer model.Buffer
	require.NoError(json.Unmarshal([]byte(bufferJson), &buffer))

	require.Equal(2, len(buffer.Tags))
	require.Equal("testvalue3", buffer.Tags["testtag3"])
	require.Equal("testvalue4", buffer.Tags["testtag4"])
	require.NotEqual(bufferETag.ETag, buffer.ETag)
}

func TestBufferSetWithInvalidETag(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	bufferId := runTygerSucceeds(t, "buffer", "create", "--tag", "testtag1=testvalue1", "--tag", "testtag2=testvalue2")
	t.Logf("Buffer ID: %s", bufferId)

	runTygerSucceeds(t, "buffer", "show", bufferId)

	_, stderr, _ := runTyger("buffer", "set", bufferId, "--etag", "bad-etag")
	require.Contains(stderr, "412 Precondition Failed")

	_, stderr2, _ := runTyger("buffer", "set", "bad-bufferid", "--etag", "bad-etag")
	require.Contains(stderr2, "404 Not Found")
}

func TestBufferSetClearTags(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	bufferId := runTygerSucceeds(t, "buffer", "create", "--tag", "testtag1=testvalue1", "--tag", "testtag2=testvalue2")
	t.Logf("Buffer ID: %s", bufferId)

	bufferJson := runTygerSucceeds(t, "buffer", "set", bufferId)

	var buffer model.Buffer
	require.NoError(json.Unmarshal([]byte(bufferJson), &buffer))

	require.Equal(0, len(buffer.Tags))
}

func TestBufferList(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	uniqueId := uuid.New().String()

	bufferId1 := runTygerSucceeds(t, "buffer", "create", "--tag", "testtag1=testvalue1", "--tag", "testtag2=testvalue2", "--tag", "testtagX="+uniqueId)
	bufferId2 := runTygerSucceeds(t, "buffer", "create", "--tag", "testtag2=testvalue2", "--tag", "testtag3=testvalue3", "--tag", "testtagX="+uniqueId)
	bufferId3 := runTygerSucceeds(t, "buffer", "create", "--tag", "testtag1=testvalue1", "--tag", "testtag3=testvalue3", "--tag", "testtagX="+uniqueId)

	bufferJson := runTygerSucceeds(t, "buffer", "list", "--tag", "testtag1=testvalue1", "--tag", "testtag2=testvalue2", "--tag", "testtagX="+uniqueId)
	var buffers []model.Buffer
	require.NoError(json.Unmarshal([]byte(bufferJson), &buffers))

	require.Equal(1, len(buffers))
	require.Equal(3, len(buffers[0].Tags))
	require.Equal(bufferId1, buffers[0].Id)
	require.Equal(uniqueId, buffers[0].Tags["testtagX"])
	require.Equal("testvalue1", buffers[0].Tags["testtag1"])
	require.Equal("testvalue2", buffers[0].Tags["testtag2"])

	bufferJson = runTygerSucceeds(t, "buffer", "list", "--tag", "testtag2=testvalue2", "--tag", "testtag3=testvalue3", "--tag", "testtagX="+uniqueId)
	buffers = nil
	require.NoError(json.Unmarshal([]byte(bufferJson), &buffers))

	require.Equal(1, len(buffers))
	require.Equal(3, len(buffers[0].Tags))
	require.Equal(bufferId2, buffers[0].Id)
	require.Equal(uniqueId, buffers[0].Tags["testtagX"])
	require.Equal("testvalue2", buffers[0].Tags["testtag2"])
	require.Equal("testvalue3", buffers[0].Tags["testtag3"])

	bufferJson = runTygerSucceeds(t, "buffer", "list", "--tag", "testtag1=testvalue1", "--tag", "testtag3=testvalue3", "--tag", "testtagX="+uniqueId)
	buffers = nil
	require.NoError(json.Unmarshal([]byte(bufferJson), &buffers))

	require.Equal(1, len(buffers))
	require.Equal(3, len(buffers[0].Tags))
	require.Equal(bufferId3, buffers[0].Id)
	require.Equal(uniqueId, buffers[0].Tags["testtagX"])
	require.Equal("testvalue1", buffers[0].Tags["testtag1"])
	require.Equal("testvalue3", buffers[0].Tags["testtag3"])

	bufferJson = runTygerSucceeds(t, "buffer", "list", "--tag", "testtag1=testvalue1", "--tag", "testtagX="+uniqueId)
	require.NoError(json.Unmarshal([]byte(bufferJson), &buffers))

	require.Equal(2, len(buffers))
}

func TestBufferListWithLimit(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	uniqueId := uuid.New().String()

	runTygerSucceeds(t, "buffer", "create", "--tag", "testtagX="+uniqueId)
	runTygerSucceeds(t, "buffer", "create", "--tag", "testtagX="+uniqueId)
	runTygerSucceeds(t, "buffer", "create", "--tag", "testtagX="+uniqueId)

	bufferJson := runTygerSucceeds(t, "buffer", "list", "--limit", "1", "--tag", "testtagX="+uniqueId)
	var buffers []model.Buffer
	require.NoError(json.Unmarshal([]byte(bufferJson), &buffers))

	require.Equal(1, len(buffers))

	bufferJson = runTygerSucceeds(t, "buffer", "list", "--limit", "2", "--tag", "testtagX="+uniqueId)
	buffers = nil
	require.NoError(json.Unmarshal([]byte(bufferJson), &buffers))

	require.Equal(2, len(buffers))

	bufferJson = runTygerSucceeds(t, "buffer", "list", "--limit", "3", "--tag", "testtagX="+uniqueId)
	buffers = nil
	require.NoError(json.Unmarshal([]byte(bufferJson), &buffers))

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
			require.FailNowf(t, "run failed.", "Run '%d'. Last status: %s", snapshot.Id, *snapshot.Status)
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

	return runCommandSucceeds(t, "docker", "inspect", "testconnectivity", "--format", "{{ index .RepoDigests 0 }}")
}

func getServiceInfoContext(t *testing.T) (context.Context, settings.ServiceInfo) {
	t.Helper()
	si, err := controlplane.GetPersistedServiceInfo()
	require.NoError(t, err)
	return settings.SetServiceInfoOnContext(context.Background(), si), si
}
