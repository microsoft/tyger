//go:build integrationtest

package integrationtest

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/microsoft/tyger/cli/internal/cmd"
	"github.com/microsoft/tyger/cli/internal/controlplane"
	"github.com/microsoft/tyger/cli/internal/dataplane"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadingWhileWriting(t *testing.T) {
	t.Parallel()

	bufferName := runTygerSucceeds(t, "buffer", "create")
	writeSasUri := runTygerSucceeds(t, "buffer", "access", bufferName, "--write")
	readSasUri := runTygerSucceeds(t, "buffer", "access", bufferName)

	// start the read process
	readCommand := exec.Command("tyger", "buffer", "read", readSasUri)
	readStdErr := &bytes.Buffer{}
	readCommand.Stderr = readStdErr
	outputHasher := sha256.New()
	readCommand.Stdout = outputHasher

	assert.NoError(t, readCommand.Start(), "read command failed to start")

	// start the write process
	writeCommand := exec.Command("tyger", "buffer", "write", writeSasUri)
	inputWriter, err := writeCommand.StdinPipe()
	require.NoError(t, err)

	writeStdErr := &bytes.Buffer{}
	writeCommand.Stderr = writeStdErr

	size := 293827382
	writeCommandErrChan := make(chan error)
	go func() {
		writeCommandErrChan <- writeCommand.Run()
	}()

	inputHasher := sha256.New()
	assert.NoError(t, cmd.Gen(int64(size), io.MultiWriter(inputWriter, inputHasher)), "failed to copy data to writer process")
	inputWriter.Close()

	assert.NoError(t, <-writeCommandErrChan, "write command failed")
	t.Log(writeStdErr.String())
	assert.Nil(t, readCommand.Wait(), "read command failed")
	t.Log(readStdErr.String())

	assert.Equal(t, inputHasher.Sum(nil), outputHasher.Sum(nil), "hashes do not match")
}

func TestAccessStringIsFile(t *testing.T) {
	t.Parallel()

	bufferName := runTygerSucceeds(t, "buffer", "create")
	writeSasUri := runTygerSucceeds(t, "buffer", "access", bufferName, "--write")
	readSasUri := runTygerSucceeds(t, "buffer", "access", bufferName)

	tempDir := t.TempDir()
	writeSasUriFile := path.Join(tempDir, "write-sas-uri.txt")
	readSasUriFile := path.Join(tempDir, "read-sas-uri.txt")

	require.Nil(t, ioutil.WriteFile(writeSasUriFile, []byte(writeSasUri), 0644))
	require.Nil(t, ioutil.WriteFile(readSasUriFile, []byte(readSasUri), 0644))

	payload := []byte("hello world")

	writeCommand := exec.Command("tyger", "buffer", "write", writeSasUriFile)
	writeCommand.Stdin = bytes.NewBuffer(payload)
	writeStdErr := bytes.NewBuffer(nil)
	writeCommand.Stderr = writeStdErr
	err := writeCommand.Run()
	t.Log(writeStdErr.String())
	require.Nil(t, err, "write command failed")

	readCommand := exec.Command("tyger", "buffer", "read", readSasUriFile)
	readStdErr := bytes.NewBuffer(nil)
	readCommand.Stderr = readStdErr
	output, err := readCommand.Output()
	t.Log(readStdErr.String())
	require.Nil(t, err, "read command failed")
	assert.Equal(t, payload, output)
}

func TestNamedPipes(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		size int
	}{
		{size: 1 * 1024},
		{size: 20 * 1024 * 1024},
	}
	for _, tc := range testCases {
		tc := tc // snapshot for parallelism
		t.Run(strconv.Itoa(tc.size), func(t *testing.T) {
			t.Parallel()

			bufferName := runTygerSucceeds(t, "buffer", "create")
			writeSasUri := runTygerSucceeds(t, "buffer", "access", bufferName, "--write")
			readSasUri := runTygerSucceeds(t, "buffer", "access", bufferName)

			tempDir := t.TempDir()
			inputPipePath := path.Join(tempDir, "input-pipe")
			outputPipePath := path.Join(tempDir, "output-pipe")

			require.Nil(t, syscall.Mkfifo(inputPipePath, 0644))
			require.Nil(t, syscall.Mkfifo(outputPipePath, 0644))

			writeCommand := exec.Command("tyger", "buffer", "write", writeSasUri, "-i", inputPipePath)
			writeStdErr := bytes.NewBuffer(nil)
			writeCommand.Stderr = writeStdErr

			inputHash := make(chan []byte, 1)
			outputHash := make(chan []byte, 1)

			go func() {
				inputPipe, err := os.OpenFile(inputPipePath, os.O_WRONLY, 0644)
				require.NoError(t, err)
				defer inputPipe.Close()

				h := sha256.New()
				mw := io.MultiWriter(inputPipe, h)
				cmd.Gen(int64(tc.size), mw)
				inputHash <- h.Sum(nil)
			}()

			readCommand := exec.Command("tyger", "buffer", "read", readSasUri, "-o", outputPipePath)
			readStdErr := bytes.NewBuffer(nil)
			readCommand.Stderr = readStdErr

			go func() {
				outputPipe, err := os.OpenFile(outputPipePath, os.O_RDONLY, 0644)
				require.NoError(t, err)
				defer outputPipe.Close()

				h := sha256.New()
				io.Copy(h, outputPipe)
				outputHash <- h.Sum(nil)
			}()

			require.Nil(t, writeCommand.Start())
			require.Nil(t, readCommand.Start())

			err := writeCommand.Wait()
			t.Log(writeStdErr.String())
			require.Nil(t, err, "write command failed")

			err = readCommand.Wait()
			t.Log(readStdErr.String())
			require.Nil(t, err, "read command failed")

			assert.Equal(t, <-inputHash, <-outputHash, "hashes do not match")
		})
	}
}

func TestMissingContainer(t *testing.T) {
	t.Parallel()

	bufferName := runTygerSucceeds(t, "buffer", "create")
	readSasUri := runTygerSucceeds(t, "buffer", "access", bufferName)
	readSasUri = strings.ReplaceAll(readSasUri, bufferName, bufferName+"missing")

	_, err := exec.Command("tyger", "buffer", "read", readSasUri).Output()
	assert.NotNil(t, err)
	ee := err.(*exec.ExitError)
	errorString := string(ee.Stderr)

	assert.Contains(t, errorString, "Container validation failed")
}

func TestInvalidHashChain(t *testing.T) {
	t.Parallel()

	inputBufferId := runTygerSucceeds(t, "buffer", "create")
	writeSasUri := runTygerSucceeds(t, "buffer", "access", inputBufferId, "-w")

	// Calling buffer.write directly to make sure an invalid hash chain is generated.
	var proxyUri string
	if serviceInfo, err := controlplane.GetPersistedServiceInfo(); err == nil {
		proxyUri = serviceInfo.GetDataPlaneProxy()
	}

	inputReader := strings.NewReader("Hello")
	dataplane.Write(writeSasUri, proxyUri, dataplane.DefaultWriteDop, dataplane.DefaultBlockSize, inputReader, true)

	readSasUri := runTygerSucceeds(t, "buffer", "access", inputBufferId)

	_, stdErr, err := runTyger("buffer", "read", readSasUri)
	if err == nil {
		t.Fatal("Hash chain was valid")
	} else {
		assert.Contains(t, stdErr, "Hash chain mismatch")
	}
}

func TestHashChain(t *testing.T) {
	t.Parallel()

	inputBufferId := runTygerSucceeds(t, "buffer", "create")
	writeSasUri := runTygerSucceeds(t, "buffer", "access", inputBufferId, "-w")

	payload := []byte("hello world")
	writeCommand := exec.Command("tyger", "buffer", "write", writeSasUri)
	writeCommand.Stdin = bytes.NewBuffer(payload)
	writeStdErr := bytes.NewBuffer(nil)
	writeCommand.Stderr = writeStdErr
	err := writeCommand.Run()

	require.Nil(t, err, "write command failed")

	readSasUri := runTygerSucceeds(t, "buffer", "access", inputBufferId)

	// Direct call to the read code in order to get the headers
	var proxyUri string
	if serviceInfo, err := controlplane.GetPersistedServiceInfo(); err == nil {
		proxyUri = serviceInfo.GetDataPlaneProxy()
	}

	httpClient, err := dataplane.CreateHttpClient(proxyUri)
	if err != nil {
		t.Fatal("Failed to create http client")
	}
	container, err := dataplane.ValidateContainer(readSasUri, httpClient)
	if err != nil {
		t.Fatal("Container validation failed")
	}

	blobUri := container.GetBlobUri(0)

	ctx := log.With().Str("operation", "buffer read").Logger().WithContext(context.Background())
	var finalBlobNumber int64 = -1
	respData, err := dataplane.WaitForBlobAndDownload(ctx, dataplane.NewClientWithLoggingContext(ctx, httpClient), blobUri, 0, &finalBlobNumber)

	assert.Nil(t, err, "Couldn't read blob")

	// Calculate the MD5 hash chain for this block
	md5Hash := md5.Sum(payload)
	encodedMD5Hash := base64.StdEncoding.EncodeToString(md5Hash[:])
	md5HashChain := md5.Sum([]byte(dataplane.EncodedMD5HashChainInitalValue + encodedMD5Hash))
	encodedMD5HashChain := base64.StdEncoding.EncodeToString(md5HashChain[:])

	md5ChainHeader := respData.Header.Get("x-ms-meta-cumulative_md5_chain")

	assert.Equal(t, encodedMD5HashChain, md5ChainHeader)
}

func TestBufferMetadata(t *testing.T) {
	t.Parallel()

	inputBufferId := runTygerSucceeds(t, "buffer", "create")
	writeSasUri := runTygerSucceeds(t, "buffer", "access", inputBufferId, "-w")

	payload := []byte("hello world")
	writeCommand := exec.Command("tyger", "buffer", "write", writeSasUri)
	writeCommand.Stdin = bytes.NewBuffer(payload)
	writeStdErr := bytes.NewBuffer(nil)
	writeCommand.Stderr = writeStdErr
	err := writeCommand.Run()

	require.Nil(t, err, "write command failed")

	readSasUri := runTygerSucceeds(t, "buffer", "access", inputBufferId)

	// Direct call to the read code in order to get the headers
	var proxyUri string
	if serviceInfo, err := controlplane.GetPersistedServiceInfo(); err == nil {
		proxyUri = serviceInfo.GetDataPlaneProxy()
	}

	httpClient, err := dataplane.CreateHttpClient(proxyUri)
	if err != nil {
		t.Fatal("Failed to create http client")
	}
	container, err := dataplane.ValidateContainer(readSasUri, httpClient)
	if err != nil {
		t.Fatal("Container validation failed")
	}

	blobUri := container.GetNamedBlobUri(".bufferstart")

	ctx := log.With().Str("operation", "buffer read").Logger().WithContext(context.Background())
	respData, err := dataplane.DownloadBlob(ctx, dataplane.NewClientWithLoggingContext(ctx, httpClient), blobUri)

	assert.Nil(t, err, "Couldn't read .bufferstart")

	var bufferFormat dataplane.BufferFormat
	json.Unmarshal(respData.Data, &bufferFormat)

	assert.Equal(t, bufferFormat.Version, "0.1.0")

	blobUri = container.GetNamedBlobUri(".bufferend")

	ctx = log.With().Str("operation", "buffer read").Logger().WithContext(context.Background())
	respData, err = dataplane.DownloadBlob(ctx, dataplane.NewClientWithLoggingContext(ctx, httpClient), blobUri)

	assert.Nil(t, err, "Couldn't read .bufferend")

	var bufferFinalization dataplane.BufferFinalization
	json.Unmarshal(respData.Data, &bufferFinalization)

	assert.Equal(t, bufferFinalization.Status, "Completed")

}

func TestRunningFromPowershellRaisesWarning(t *testing.T) {
	t.Parallel()

	corruptionWarning := "PowerShell I/O redirection may corrupt binary data"

	bufferId := runTygerSucceeds(t, "buffer", "create")

	cmd := exec.Command("pwsh", "-Command", fmt.Sprintf("tyger buffer write %s", bufferId))
	cmd.Stdin = bytes.NewBuffer([]byte("hello world"))
	stdErrBuffer := bytes.NewBuffer(nil)
	cmd.Stderr = stdErrBuffer

	assert.Nil(t, cmd.Run())
	assert.Contains(t, stdErrBuffer.String(), corruptionWarning)

	cmd = exec.Command("tyger", "buffer", "read", bufferId)
	stdErrBuffer = bytes.NewBuffer(nil)
	cmd.Stderr = stdErrBuffer

	assert.Nil(t, cmd.Run())
	assert.NotContains(t, stdErrBuffer.String(), corruptionWarning)
}
