//go:build e2e

//go:generate go run github.com/deepmap/oapi-codegen/cmd/oapi-codegen --package=e2e --generate types,client -o client.gen.go ../../openapi.yaml
package e2e_test

import (
	"context"
	"io"
	"log"
	"net/http"
	"testing"
	"time"

	"dev.azure.com/msresearch/compimag/_git/tyger/test/e2e"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/pointer"
)

var (
	client *e2e.ClientWithResponses
)

func init() {
	var err error
	client, err = e2e.NewClientWithResponses("http://tyger.localdev.me")
	if err != nil {
		log.Fatal(err)
	}
}

func TestEndToEnd(t *testing.T) {
	require := require.New(t)

	// create a codespec
	const codespecName = "testcodespec"

	bufferPrameters := []e2e.BufferParameter{
		{Name: "input"},
		{Name: "output", Writeable: pointer.Bool(true)},
	}
	args := []string{"-r", "$(INPUT_BUFFER_URI_FILE)", "-w", "$(OUTPUT_BUFFER_URI_FILE)"}
	codespec := e2e.Codespec{
		BufferParameters: &bufferPrameters,
		Image:            "testrecon:test",
		Args:             &args,
	}

	codespecResponse, err := client.UpsertCodespecWithResponse(context.Background(), codespecName, e2e.UpsertCodespecJSONRequestBody(codespec))
	require.Nil(err)
	require.True(codespecResponse.StatusCode() == http.StatusCreated || codespecResponse.StatusCode() == http.StatusOK, "unexpected status code %d", codespecResponse.StatusCode())

	// create an input buffer
	createBufferResponse, err := client.CreateBufferWithResponse(context.Background(), new(e2e.CreateBufferJSONRequestBody))
	require.Nil(err)
	inputBufferId := createBufferResponse.JSON201.Id

	// get a SAS token to be able to write to it
	sasResponse, err := client.GetBufferAccessUriWithResponse(context.Background(), inputBufferId, &e2e.GetBufferAccessUriParams{Writeable: pointer.Bool(true)})
	require.Nil(err)
	inputSasUri := sasResponse.JSON201.Uri

	// create an output buffer
	createBufferResponse, err = client.CreateBufferWithResponse(context.Background(), new(e2e.CreateBufferJSONRequestBody))
	require.Nil(err)
	outputBufferId := createBufferResponse.JSON201.Id

	// get a SAS token to be able to read from it
	sasResponse, err = client.GetBufferAccessUriWithResponse(context.Background(), outputBufferId, &e2e.GetBufferAccessUriParams{Writeable: pointer.Bool(false)})
	require.Nil(err)
	outputSasUri := sasResponse.JSON201.Uri

	// write to the input buffer using the SAS URI
	inputContainerClient, err := azblob.NewContainerClientWithNoCredential(inputSasUri, nil)
	require.Nil(err)
	blobClient := inputContainerClient.NewBlockBlobClient("0")
	_, err = blobClient.UploadBufferToBlockBlob(context.Background(), []byte("Hello"), azblob.HighLevelUploadToBlockBlobOption{})
	require.Nil(err, err)

	// create run
	bufferArgs := e2e.NewRun_Buffers{
		AdditionalProperties: map[string]string{
			"input":  inputBufferId,
			"output": outputBufferId,
		},
	}

	newRun := e2e.NewRun{
		Buffers:  &bufferArgs,
		Codespec: codespecName,
	}

	createRunResponse, err := client.CreateRunWithResponse(context.Background(), e2e.CreateRunJSONRequestBody(newRun))
	require.Nil(err)
	require.Equal(http.StatusCreated, createRunResponse.StatusCode())

	runId := createRunResponse.JSON201.Id

	for i := 0; ; i++ {
		runResponse, err := client.GetRunWithResponse(context.Background(), runId)
		require.Nil(err)
		require.Equal(http.StatusOK, runResponse.StatusCode())

		if runResponse.JSON200.Status != nil && *runResponse.JSON200.Status == "Completed" {
			break
		}

		time.Sleep(time.Millisecond * 200)

		if i == 100 {
			require.FailNowf("run failed to complete.", "Last status: %v", *runResponse.JSON200.Status)
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
	resp, err := client.GetLatestCodespec(context.Background(), "missing")
	require.Nil(err)
	require.NotEmpty(resp.Header.Get("X-Request-ID"))
}
