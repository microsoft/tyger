// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build integrationtest

package integrationtest

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/microsoft/tyger/cli/internal/client"
	"github.com/microsoft/tyger/cli/internal/controlplane"
	"github.com/microsoft/tyger/cli/internal/dataplane"
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
	assert.NoError(t, dataplane.Gen(int64(size), io.MultiWriter(inputWriter, inputHasher)), "failed to copy data to writer process")
	inputWriter.Close()

	assert.NoError(t, <-writeCommandErrChan, "write command failed")
	t.Log(writeStdErr.String())
	assert.Nil(t, readCommand.Wait(), "read command failed")
	t.Log(readStdErr.String())

	assert.Equal(t, inputHasher.Sum(nil), outputHasher.Sum(nil), "hashes do not match")
}

func TestTrickleLatencyWithFlushInterval(t *testing.T) {
	t.Parallel()

	bufferName := runTygerSucceeds(t, "buffer", "create")
	writeSasUri := runTygerSucceeds(t, "buffer", "access", bufferName, "--write")
	readSasUri := runTygerSucceeds(t, "buffer", "access", bufferName)

	// start the read process
	readCommand := exec.Command("tyger", "buffer", "read", readSasUri)
	outputReader, err := readCommand.StdoutPipe()
	require.NoError(t, err)
	readStdErr := &bytes.Buffer{}
	readCommand.Stderr = readStdErr

	assert.NoError(t, readCommand.Start(), "read command failed to start")

	// start the write process
	writeCommand := exec.Command("tyger", "buffer", "write", writeSasUri, "--flush-interval", "1s")
	inputWriter, err := writeCommand.StdinPipe()
	require.NoError(t, err)

	writeStdErr := &bytes.Buffer{}
	writeCommand.Stderr = writeStdErr

	linesWritten := 0

	go func() {
		defer inputWriter.Close()
		start := time.Now()
		end := start.Add(6 * time.Second)
		for now := start; now.Compare(end) < 0; now = time.Now() {
			_, err := inputWriter.Write([]byte(fmt.Sprintf("%s\n", now.Format(time.RFC3339Nano))))
			require.NoError(t, err)
			linesWritten++
			time.Sleep(10 * time.Millisecond)
		}
	}()

	writeCommandErrChan := make(chan error)
	go func() {
		writeCommandErrChan <- writeCommand.Run()
	}()

	linesRead := 0
	// read the output line by line
	scanner := bufio.NewScanner(outputReader)
	for scanner.Scan() {
		line := scanner.Text()
		parsedTime, err := time.Parse(time.RFC3339Nano, line)
		require.NoError(t, err)
		// on GitHub Actions runners, there can be significant latency when this is running in Docker mode and all tests are running in parallel
		require.WithinDuration(t, time.Now(), parsedTime, 10*time.Second)
		linesRead++
	}

	t.Log(writeStdErr.String())

	assert.NoError(t, <-writeCommandErrChan, "write command failed")

	assert.Nil(t, readCommand.Wait(), "read command failed")
	require.Equal(t, linesWritten, linesRead, "number of lines written and read do not match")
	t.Log(readStdErr.String())
}

func TestAccessStringIsFile(t *testing.T) {
	t.Parallel()

	bufferName := runTygerSucceeds(t, "buffer", "create")
	writeSasUri := runTygerSucceeds(t, "buffer", "access", bufferName, "--write")
	readSasUri := runTygerSucceeds(t, "buffer", "access", bufferName)

	tempDir := t.TempDir()
	writeSasUriFile := path.Join(tempDir, "write-sas-uri.txt")
	readSasUriFile := path.Join(tempDir, "read-sas-uri.txt")

	require.Nil(t, os.WriteFile(writeSasUriFile, []byte(writeSasUri), 0644))
	require.Nil(t, os.WriteFile(readSasUriFile, []byte(readSasUri), 0644))

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
				dataplane.Gen(int64(tc.size), mw)
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
	readSasUri, err := url.Parse(runTygerSucceeds(t, "buffer", "access", bufferName))
	require.NoError(t, err)

	client := newInterceptingHttpClient(t, func(req *http.Request, inner http.RoundTripper) (*http.Response, error) {
		resp, err := inner.RoundTrip(req)
		if err != nil {
			return resp, err
		}

		resp.StatusCode = http.StatusNotFound
		resp.Header.Set("x-ms-error-code", "ContainerNotFound")
		return resp, nil
	})

	err = dataplane.Write(context.Background(), readSasUri, strings.NewReader("Hello"), dataplane.WithWriteHttpClient(client))
	require.ErrorContains(t, err, "the buffer does not exist")

	err = dataplane.Read(context.Background(), readSasUri, io.Discard, dataplane.WithReadHttpClient(client))
	require.ErrorContains(t, err, "the buffer does not exist")
}

func TestInvalidHashChain(t *testing.T) {
	t.Parallel()

	inputBufferId := runTygerSucceeds(t, "buffer", "create")
	writeSasUri, err := url.Parse(runTygerSucceeds(t, "buffer", "access", inputBufferId, "-w"))
	require.NoError(t, err)

	inputReader := strings.NewReader("Hello")

	httpClient := newInterceptingHttpClient(t, func(req *http.Request, inner http.RoundTripper) (*http.Response, error) {
		req.Header.Set(dataplane.HashChainHeader, "invalid")
		return inner.RoundTrip(req)
	})

	err = dataplane.Write(context.Background(), writeSasUri, inputReader, dataplane.WithWriteHttpClient(httpClient))
	require.NoError(t, err, "Failed to write data")

	readSasUri := runTygerSucceeds(t, "buffer", "access", inputBufferId)

	_, stdErr, err := runTyger("buffer", "read", readSasUri)
	assert.Contains(t, stdErr, "hash chain mismatch")
}

func TestMd5HashMismatchOnWrite(t *testing.T) {
	t.Parallel()

	inputBufferId := runTygerSucceeds(t, "buffer", "create")
	writeSasUri, err := url.Parse(runTygerSucceeds(t, "buffer", "access", inputBufferId, "-w"))
	require.NoError(t, err)
	inputReader := strings.NewReader("Hello")
	httpClient := newInterceptingHttpClient(t, func(req *http.Request, inner http.RoundTripper) (*http.Response, error) {
		md5Hash := md5.Sum([]byte("invalid"))
		encodedMD5Hash := base64.StdEncoding.EncodeToString(md5Hash[:])
		req.Header.Set(dataplane.ContentMD5Header, encodedMD5Hash)
		return inner.RoundTrip(req)
	})

	err = dataplane.Write(context.Background(), writeSasUri, inputReader, dataplane.WithWriteHttpClient(httpClient))
	require.ErrorContains(t, err, "MD5 mismatch")
}

func TestMd5HashMismatchOnWriteRetryAndRecover(t *testing.T) {
	t.Parallel()

	inputBufferId := runTygerSucceeds(t, "buffer", "create")
	writeSasUri, err := url.Parse(runTygerSucceeds(t, "buffer", "access", inputBufferId, "-w"))
	require.NoError(t, err)
	inputReader := strings.NewReader("Hello")

	failedUris := make(map[string]any)
	httpClient := newInterceptingHttpClient(t, func(req *http.Request, inner http.RoundTripper) (*http.Response, error) {
		if _, ok := failedUris[req.URL.String()]; ok {
			return inner.RoundTrip(req)
		}

		failedUris[req.URL.String()] = nil
		md5Hash := md5.Sum([]byte("invalid"))
		encodedMD5Hash := base64.StdEncoding.EncodeToString(md5Hash[:])
		req.Header.Set(dataplane.ContentMD5Header, encodedMD5Hash)
		return inner.RoundTrip(req)
	})

	err = dataplane.Write(context.Background(), writeSasUri, inputReader, dataplane.WithWriteHttpClient(httpClient), dataplane.WithWriteDop(1))
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(failedUris), 2)
}

func TestMd5HashMismatchOnRead(t *testing.T) {
	t.Parallel()

	inputBufferId := runTygerSucceeds(t, "buffer", "create")
	writeSasUri, err := url.Parse(runTygerSucceeds(t, "buffer", "access", inputBufferId, "-w"))
	require.NoError(t, err)
	inputReader := strings.NewReader("Hello")

	require.NoError(t, dataplane.Write(context.Background(), writeSasUri, inputReader))

	httpClient := newInterceptingHttpClient(t, func(req *http.Request, inner http.RoundTripper) (*http.Response, error) {
		resp, err := inner.RoundTrip(req)
		if err != nil {
			return resp, err
		}

		md5Hash := md5.Sum([]byte("invalid"))
		encodedMD5Hash := base64.StdEncoding.EncodeToString(md5Hash[:])
		resp.Header.Set(dataplane.ContentMD5Header, encodedMD5Hash)
		return resp, nil
	})

	err = dataplane.Read(context.Background(), writeSasUri, io.Discard, dataplane.WithReadHttpClient(httpClient))
	require.ErrorContains(t, err, "MD5 mismatch")
}

func TestMd5HashMismatchOnReadRetryAndRecover(t *testing.T) {
	t.Parallel()

	inputBufferId := runTygerSucceeds(t, "buffer", "create")
	writeSasUri, err := url.Parse(runTygerSucceeds(t, "buffer", "access", inputBufferId, "-w"))
	require.NoError(t, err)
	inputReader := strings.NewReader("Hello")
	require.NoError(t, dataplane.Write(context.Background(), writeSasUri, inputReader))

	failedUris := make(map[string]any)
	mutex := sync.Mutex{}
	httpClient := newInterceptingHttpClient(t, func(req *http.Request, inner http.RoundTripper) (*http.Response, error) {
		resp, err := inner.RoundTrip(req)
		if err != nil {
			return resp, err
		}

		mutex.Lock()
		defer mutex.Unlock()
		if _, ok := failedUris[req.URL.String()]; ok {
			return resp, err
		}

		failedUris[req.URL.String()] = nil
		md5Hash := md5.Sum([]byte("invalid"))
		encodedMD5Hash := base64.StdEncoding.EncodeToString(md5Hash[:])
		resp.Header.Set(dataplane.ContentMD5Header, encodedMD5Hash)
		return resp, nil
	})

	err = dataplane.Read(context.Background(), writeSasUri, io.Discard, dataplane.WithReadHttpClient(httpClient), dataplane.WithReadDop(1))
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(failedUris), 2)
}

func TestCancellationOnWrite(t *testing.T) {
	t.Parallel()

	inputBufferId := runTygerSucceeds(t, "buffer", "create", "--tag", fmt.Sprintf("test=%s", t.Name()))
	writeSasUri, err := url.Parse(runTygerSucceeds(t, "buffer", "access", inputBufferId, "-w"))
	require.NoError(t, err)
	inputReader := &infiniteReader{}

	errorChan := make(chan error, 1)
	go func() {
		errorChan <- dataplane.Read(context.Background(), writeSasUri, io.Discard)
	}()

	writeCtx, cancel := context.WithCancel(context.Background())

	// cancel as soon as we have written the start metadata
	writeClient := newInterceptingHttpClient(t, func(req *http.Request, inner http.RoundTripper) (*http.Response, error) {
		if strings.Contains(req.URL.Path, dataplane.StartMetadataBlobName) || strings.Contains(req.URL.Path, dataplane.EndMetadataBlobName) {
			return inner.RoundTrip(req)
		}

		cancel()
		return nil, writeCtx.Err()
	})

	defer cancel()
	err = dataplane.Write(writeCtx, writeSasUri, inputReader, dataplane.WithWriteHttpClient(writeClient), dataplane.WithWriteMetadataEndWriteTimeout(time.Minute))
	assert.ErrorIs(t, err, context.Canceled)

	assert.ErrorContains(t, <-errorChan, dataplane.ErrBufferFailedState.Error())
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

func TestBufferDoubleWriteFailure(t *testing.T) {
	t.Parallel()

	inputBufferId := runTygerSucceeds(t, "buffer", "create")
	inputSasUri := runTygerSucceeds(t, "buffer", "access", inputBufferId, "-w")

	runCommandSucceeds(t, "sh", "-c", fmt.Sprintf(`echo "Hello" | tyger buffer write "%s"`, inputSasUri))

	_, _, err := runCommand("sh", "-c", fmt.Sprintf(`echo "Hello" | tyger buffer write "%s"`, inputSasUri))
	require.Error(t, err, "Second call to buffer write succeeded")

	var exitError *exec.ExitError
	require.ErrorAs(t, err, &exitError)
	require.NotEqual(t, 0, exitError.ExitCode(), "Second call to buffer write had unexpected exit code")
}

func newInterceptingHttpClient(t *testing.T, roundtrip func(req *http.Request, inner http.RoundTripper) (*http.Response, error)) *retryablehttp.Client {
	tygerClient, err := controlplane.GetClientFromCache()
	require.NoError(t, err)

	c := client.CloneRetryableClient(tygerClient.DataPlaneClient.Client)
	c.HTTPClient.Transport = &httpInterceptorRountripper{RoundTripper: c.HTTPClient.Transport, interceptor: roundtrip}
	return c
}

type httpInterceptorRountripper struct {
	http.RoundTripper
	interceptor func(req *http.Request, inner http.RoundTripper) (*http.Response, error)
}

func (i *httpInterceptorRountripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if i.interceptor != nil {
		return i.interceptor(req, i.RoundTripper)
	}

	return i.RoundTripper.RoundTrip(req)
}

type infiniteReader struct {
}

func (r *infiniteReader) Read(p []byte) (n int, err error) {
	return len(p), nil
}
