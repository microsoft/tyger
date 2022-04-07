//go:build e2e

package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/clicontext"
	"dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/cmd"
	"dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/model"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/andreyvit/diff"
	"github.com/stretchr/testify/require"
)

func init() {
	stdout, stderr, err := runTyger("login", "status")
	if err != nil {
		fmt.Fprintln(os.Stderr, stderr, stdout)
		log.Fatal(err)
	}
}

func TestEndToEnd(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	// create a codespec
	const codespecName = "testcodespec"
	digest := runCommandSuceeds(t, "docker", "inspect", "testrecon", "--format", "{{ index .RepoDigests 0 }}")

	runTygerSuceeds(t,
		"create", "codespec", codespecName,
		"-i=input", "-o=output",
		"--image", digest,
		"--",
		"-r", "$(INPUT_BUFFER_URI_FILE)", "-w", "$(OUTPUT_BUFFER_URI_FILE)")

	// create an input buffer and a SAS token to be able to write to it
	inputBufferId := runTygerSuceeds(t, "create", "buffer")
	inputSasUri := runTygerSuceeds(t, "access", "buffer", inputBufferId, "-w")

	// create and output buffer and a SAS token to be able to read from it
	outputBufferId := runTygerSuceeds(t, "create", "buffer")
	outputSasUri := runTygerSuceeds(t, "access", "buffer", outputBufferId)

	// write to the input buffer using the SAS URI
	inputContainerClient, err := azblob.NewContainerClientWithNoCredential(inputSasUri, nil)
	require.Nil(err)
	blobClient := inputContainerClient.NewBlockBlobClient("0")
	_, err = blobClient.UploadBufferToBlockBlob(context.Background(), []byte("Hello"), azblob.HighLevelUploadToBlockBlobOption{})
	require.Nil(err, err)

	// create run
	runId := runTygerSuceeds(t, "create", "run", "--codespec", codespecName,
		"-b", fmt.Sprintf("input=%s", inputBufferId),
		"-b", fmt.Sprintf("output=%s", outputBufferId))

	waitForRunSuccess(t, runId)

	outputContainerClient, err := azblob.NewContainerClientWithNoCredential(outputSasUri, nil)
	if err != nil {
		log.Fatal(err)
	}

	outputBlockBlobClient := outputContainerClient.NewBlockBlobClient("0")
	inputResp, err := outputBlockBlobClient.Download(context.Background(), nil)
	if err != nil {
		log.Fatal(err)
	}

	outputBytes, err := io.ReadAll(inputResp.Body(&azblob.RetryReaderOptions{}))
	if err != nil {
		log.Fatal(err)
	}

	require.Equal("Hello: Bonjour", string(outputBytes))
}

// Verify that a run using a codespec that requires a GPU
// is scheduled on a node with one.
func TestGpuResourceRequirement(t *testing.T) {
	t.Parallel()

	const codespecName = "gputestcodespec"
	runTygerSuceeds(t,
		"create", "codespec", codespecName,
		"--image", "nvidia/cuda:11.0-base",
		"--gpu", "1",
		"--command",
		"--",
		"bash", "-c", "[[ $(nvidia-smi -L | wc -l) == 1 ]]") // verify that a GPU is available

	// create run
	runId := runTygerSuceeds(t, "create", "run", "--codespec", codespecName)

	waitForRunSuccess(t, runId)
}

// Verify that a run using a codespec that does not require a GPU
// is not scheduled on a node with one.
func TestNoGpuResourceRequirement(t *testing.T) {
	t.Parallel()

	const codespecName = "nogputestcodespec"
	runTygerSuceeds(t,
		"create", "codespec", codespecName,
		"--image", "nvidia/cuda:11.0-base",
		"--command",
		"--",
		"bash", "-c", "[[ ! $(nvidia-smi) ]]") // verify that no GPU is available

	// create run
	runId := runTygerSuceeds(t, "create", "run", "--codespec", codespecName)

	waitForRunSuccess(t, runId)
}

func TestTargetGpuNodePool(t *testing.T) {
	t.Parallel()

	codespecName := t.Name()
	runTygerSuceeds(t,
		"create", "codespec", codespecName,
		"--image", "nvidia/cuda:11.0-base",
		"--command",
		"--",
		"bash", "-c", "[[ $(nvidia-smi -L | wc -l) == 1 ]]") // verify that a GPU is available

	// create run
	runId := runTygerSuceeds(t, "create", "run", "--codespec", codespecName, "--node-pool", "gpunp")

	waitForRunSuccess(t, runId)
}

func TestTargetCpuNodePool(t *testing.T) {
	t.Parallel()

	codespecName := t.Name()
	runTygerSuceeds(t,
		"create", "codespec", codespecName,
		"--image", "nvidia/cuda:11.0-base",
		"--command",
		"--",
		"bash", "-c", "[[ ! $(nvidia-smi) ]]") // verify that no GPU is available

	// create run
	runId := runTygerSuceeds(t, "create", "run", "--codespec", codespecName, "--node-pool", "cpunp")

	waitForRunSuccess(t, runId)
}

func TestTargetingInvalidClusterReturnsError(t *testing.T) {
	t.Parallel()

	codespecName := t.Name()
	runTygerSuceeds(t,
		"create", "codespec", codespecName,
		"--image", "ubuntu")

	_, stderr, _ := runTyger("create", "run", "--codespec", codespecName, "--cluster", "invalid")
	require.Contains(t, stderr, "Unknown cluster")
}

func TestTargetingInvalidNodePoolReturnsError(t *testing.T) {
	t.Parallel()

	codespecName := t.Name()
	runTygerSuceeds(t,
		"create", "codespec", codespecName,
		"--image", "ubuntu")

	_, stderr, _ := runTyger("create", "run", "--codespec", codespecName, "--node-pool", "invalid")
	require.Contains(t, stderr, "Unknown nodepool")
}

func TestTargetCpuNodePoolWithGpuResourcesReturnsError(t *testing.T) {
	t.Parallel()

	codespecName := t.Name()
	runTygerSuceeds(t,
		"create", "codespec", codespecName,
		"--image", "ubuntu",
		"--gpu", "1")

	_, stderr, _ := runTyger("create", "run", "--codespec", codespecName, "--node-pool", "cpunp")
	require.Contains(t, stderr, "does not have GPUs and cannot satisfy GPU request")
}

func TestUnrecognizedFieldsRejected(t *testing.T) {
	t.Parallel()
	require := require.New(t)

	codespec := model.Codespec{}
	requestBody := map[string]string{"image": "x"}
	_, err := cmd.InvokeRequest(http.MethodPut, "v1/codespecs/tcs", requestBody, &codespec, false)
	require.Nil(err)

	requestBody["unknownField"] = "y"
	_, err = cmd.InvokeRequest(http.MethodPut, "v1/codespecs/tcs", requestBody, &codespec, false)
	require.NotNil(err)
}

func TestResponseContainsRequestIdHeader(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	_, stderr, _ := runTyger("get", "codespec", "missing")

	require.Contains(stderr, "Request-Id")
}

func TestOpenApiSpecIsAsExpected(t *testing.T) {
	t.Parallel()
	ctx, err := clicontext.GetCliContext()
	require.Nil(t, err)
	swaggerUri := fmt.Sprintf("%s/swagger/v1/swagger.yaml", ctx.GetServerUri())
	resp, err := http.Get(swaggerUri)
	require.Nil(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	actualBytes, err := io.ReadAll(resp.Body)
	require.Nil(t, err)
	expectedFilePath, err := filepath.Abs("expected_openapi_spec.yaml")
	require.Nil(t, err)
	expectedBytes, err := ioutil.ReadFile(expectedFilePath)
	require.Nil(t, err)

	if a, e := strings.TrimSpace(string(actualBytes)), strings.TrimSpace(string(expectedBytes)); a != e {
		t.Errorf("Result not as expected. To update, run `curl %s > %s`\n\nDiff:%v",
			swaggerUri,
			expectedFilePath,
			diff.LineDiff(e, a))
	}
}

func TestAuthenticationRequired(t *testing.T) {
	t.Parallel()
	ctx, err := clicontext.GetCliContext()
	require.Nil(t, err)
	resp, err := http.Get(fmt.Sprintf("%s/v1/runs/abc", ctx.GetServerUri()))
	require.Nil(t, err)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func runCommand(command string, args ...string) (stdout string, stderr string, err error) {
	cmd := exec.Command(command, args...)
	var outb, errb bytes.Buffer
	cmd.Stdout = &outb
	cmd.Stderr = &errb
	err = cmd.Run()

	// strip away newline suffix
	stdout = string(bytes.TrimSuffix(outb.Bytes(), []byte{'\n'}))

	stderr = string(errb.String())
	return
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

func runTyger(args ...string) (stdout string, stderr string, err error) {
	args = append([]string{"-v"}, args...)
	return runCommand("tyger", args...)
}

func runTygerSuceeds(t *testing.T, args ...string) string {
	args = append([]string{"-v"}, args...)
	return runCommandSuceeds(t, "tyger", args...)
}

func waitForRunSuccess(t *testing.T, runId string) {
	start := time.Now()
	for {
		runJson := runTygerSuceeds(t, "get", "run", runId)
		run := model.Run{}
		require.Nil(t, json.Unmarshal([]byte(runJson), &run))
		if run.Status == "Completed" {
			break
		}

		switch run.Status {
		case "Pending":
		case "ContainerCreating":
		case "Running":
			break
		default:
			require.FailNowf(t, "run failed.", "Run '%s'. Last status: %s", run.Id, run.Status)
		}

		elapsed := time.Now().Sub(start)

		switch {
		case elapsed < 10*time.Second:
			time.Sleep(time.Millisecond * 250)
		case elapsed < time.Minute:
			time.Sleep(time.Second)
		case elapsed < 15*time.Minute:
			time.Sleep(10 * time.Second)
		default:
			require.FailNowf(t, "timed out waiting for run %s.", "Run '%s'. Last status: %s", run.Id, run.Status)
		}
	}
}
