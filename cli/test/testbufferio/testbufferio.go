package main

// intended to be executed in a container as a test "run"

import (
	"context"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/google/uuid"
	"github.com/hashicorp/go-retryablehttp"
	"github.com/johnstairs/pathenvconfig"
)

func main() {
	readFromFlag := flag.String("r", "", "read from")
	writeToFlag := flag.String("w", "", "write to")

	flag.Parse()

	if *readFromFlag == "" {
		log.Fatal("-r flag must be specified")
	}

	if *writeToFlag == "" {
		log.Fatal("-w flag must be specified")
	}
	readFrom := *readFromFlag
	writeTo := *writeToFlag

	config := configSpec{}
	err := pathenvconfig.Process("", &config)
	if err != nil {
		log.Fatal(err)
	}

	if err := verifyStorageServerConnectivity(config); err != nil {
		log.Fatal(err)
	}

	bytes, err := ioutil.ReadFile(readFrom)
	if err != nil {
		log.Fatal(err)
	}
	readFrom = string(bytes)

	bytes, err = ioutil.ReadFile(writeTo)
	if err != nil {
		log.Fatal(err)
	}
	writeTo = string(bytes)

	inputContainerClient, err := azblob.NewContainerClientWithNoCredential(readFrom, nil)
	if err != nil {
		log.Fatal(err)
	}

	inputBlockBlobClient := inputContainerClient.NewBlockBlobClient("0")
	inputResp, err := inputBlockBlobClient.Download(context.Background(), nil)
	if err != nil {
		log.Fatal(err)
	}

	inputBytes, err := io.ReadAll(inputResp.Body(&azblob.RetryReaderOptions{}))
	if err != nil {
		log.Fatal(err)
	}

	outputString := fmt.Sprintf("%s: Bonjour", string(inputBytes))

	outputContainerClient, err := azblob.NewContainerClientWithNoCredential(writeTo, nil)
	if err != nil {
		log.Fatal(err)
	}

	outputBlockBlobClient := outputContainerClient.NewBlockBlobClient("0")
	_, err = outputBlockBlobClient.UploadBufferToBlockBlob(context.Background(), []byte(outputString), azblob.HighLevelUploadToBlockBlobOption{})
	if err != nil {
		log.Fatal(err)
	}
}

func verifyStorageServerConnectivity(config configSpec) error {
	subject := uuid.New()
	query := fmt.Sprintf("subject=%s&name=test", subject)

	createBlobUri := fmt.Sprintf("%s/v1/blobs/data?%s", config.MrdStorageUri, query)
	const content = "my data"
	resp, err := retryablehttp.Post(createBlobUri, "text/plain", strings.NewReader(content))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("unexpected status code %d from storage server write", resp.StatusCode)
	}

	getLatestBlobUri := fmt.Sprintf("%s/v1/blobs/data/latest?%s", config.MrdStorageUri, query)

	resp, err = retryablehttp.Get(getLatestBlobUri)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status code %d from storage server read", resp.StatusCode)
	}

	responseContent, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	responseContentString := string(responseContent)
	if responseContentString != content {
		return fmt.Errorf("unexpected blob content: %s", responseContentString)
	}

	return nil
}

type configSpec struct {
	MrdStorageUri string `required:"true"`
}
