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
	require := require.New(t)

	// create a codespec
	const codespecName = "testcodespec"

	runTygerSuceeds(t,
		"create", "codespec", codespecName,
		"-i=input", "-o=output",
		"--image", "testrecon:test",
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

	for i := 0; ; i++ {
		runJson := runTygerSuceeds(t, "get", "run", runId)
		run := model.Run{}
		require.Nil(json.Unmarshal([]byte(runJson), &run))
		if run.Status == "Completed" {
			break
		}

		time.Sleep(time.Millisecond * 200)

		if i == 100 {
			require.FailNowf("run failed to complete.", "Last status: %s", run.Status)
		}
	}

	outputContainerClient, err := azblob.NewContainerClientWithNoCredential(outputSasUri, nil)
	if err != nil {
		log.Fatal(err)
	}

	outputBlockBlobClient := outputContainerClient.NewBlockBlobClient("0")
	inputResp, err := outputBlockBlobClient.Download(context.Background(), nil)
	if err != nil {
		log.Fatal(err)
	}

	outputBytes, err := io.ReadAll(inputResp.Body(azblob.RetryReaderOptions{}))
	if err != nil {
		log.Fatal(err)
	}

	require.Equal("Hello: Bonjour", string(outputBytes))
}

func TestResponseContainsRequestIdHeader(t *testing.T) {
	require := require.New(t)
	_, stderr, _ := runTyger("get", "codespec", "missing")

	require.Contains(stderr, "Request-Id")
}

func TestOpenApiSpecIsAsExpected(t *testing.T) {
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
	ctx, err := clicontext.GetCliContext()
	require.Nil(t, err)
	resp, err := http.Get(fmt.Sprintf("%s/v1/runs/abc", ctx.GetServerUri()))
	require.Nil(t, err)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func runTyger(args ...string) (stdout string, stderr string, err error) {
	args = append([]string{"-v"}, args...)
	cmd := exec.Command("tyger", args...)
	var outb, errb bytes.Buffer
	cmd.Stdout = &outb
	cmd.Stderr = &errb
	err = cmd.Run()

	// strip away newline suffix
	stdout = string(bytes.TrimSuffix(outb.Bytes(), []byte{'\n'}))

	stderr = string(errb.String())
	return
}

func runTygerSuceeds(t *testing.T, args ...string) string {
	stdout, stderr, err := runTyger(args...)
	if err != nil {
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			t.Log(stderr)
			t.Log(stdout)
			t.Errorf("Unexpected error code %d", exitError.ExitCode())
			t.FailNow()
		}
		t.Errorf("Failure executing tyger: %v", err)
		t.FailNow()
	}

	return stdout
}
