//go:build integrationtest

package bufferproxy_test

import (
	"bytes"
	"crypto/sha256"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"dev.azure.com/msresearch/compimag/_git/tyger/cli/integrationtest"
	bufferproxy "dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/buffer-proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadingWhileWriting(t *testing.T) {
	t.Parallel()

	bufferName := integrationtest.RunTygerSuceeds(t, "buffer", "create")
	writeSasUri := integrationtest.RunTygerSuceeds(t, "buffer", "access", bufferName, "--write")
	readSasUri := integrationtest.RunTygerSuceeds(t, "buffer", "access", bufferName)

	writeCommand := exec.Command("buffer-proxy", "write", writeSasUri)
	inputWriter, err := writeCommand.StdinPipe()
	writeStdErr := bytes.NewBuffer(nil)
	writeCommand.Stderr = writeStdErr
	require.Nil(t, err)

	inputHash := make(chan []byte, 1)
	outputHash := make(chan []byte, 1)

	size := 293827382
	go func() {
		defer inputWriter.Close()
		h := sha256.New()
		mw := io.MultiWriter(inputWriter, h)
		bufferproxy.Gen(int64(size), mw)
		inputHash <- h.Sum(nil)
	}()

	readCommand := exec.Command("buffer-proxy", "read", readSasUri)
	readStdErr := bytes.NewBuffer(nil)
	readCommand.Stderr = readStdErr
	outputReader, err := readCommand.StdoutPipe()
	require.Nil(t, err)

	go func() {
		h := sha256.New()
		io.Copy(h, outputReader)
		outputHash <- h.Sum(nil)
	}()

	err = writeCommand.Start()
	require.Nil(t, err)
	err = readCommand.Start()
	require.Nil(t, err)

	err = writeCommand.Wait()
	t.Log(writeStdErr.String())
	require.Nil(t, err, "write command failed")

	err = readCommand.Wait()
	t.Log(readStdErr.String())
	require.Nil(t, err, "read command failed")

	assert.Equal(t, <-inputHash, <-outputHash, "hashes do not match")
}

func TestAccessStringIsFile(t *testing.T) {
	t.Parallel()

	bufferName := integrationtest.RunTygerSuceeds(t, "buffer", "create")
	writeSasUri := integrationtest.RunTygerSuceeds(t, "buffer", "access", bufferName, "--write")
	readSasUri := integrationtest.RunTygerSuceeds(t, "buffer", "access", bufferName)

	tempDir := t.TempDir()
	writeSasUriFile := path.Join(tempDir, "write-sas-uri.txt")
	readSasUriFile := path.Join(tempDir, "read-sas-uri.txt")

	require.Nil(t, ioutil.WriteFile(writeSasUriFile, []byte(writeSasUri), 0644))
	require.Nil(t, ioutil.WriteFile(readSasUriFile, []byte(readSasUri), 0644))

	payload := []byte("hello world")

	writeCommand := exec.Command("buffer-proxy", "write", writeSasUriFile)
	writeCommand.Stdin = bytes.NewBuffer(payload)
	writeStdErr := bytes.NewBuffer(nil)
	writeCommand.Stderr = writeStdErr
	err := writeCommand.Run()
	t.Log(writeStdErr.String())
	require.Nil(t, err, "write command failed")

	readCommand := exec.Command("buffer-proxy", "read", readSasUriFile)
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

			bufferName := integrationtest.RunTygerSuceeds(t, "buffer", "create")
			writeSasUri := integrationtest.RunTygerSuceeds(t, "buffer", "access", bufferName, "--write")
			readSasUri := integrationtest.RunTygerSuceeds(t, "buffer", "access", bufferName)

			tempDir := t.TempDir()
			inputPipePath := path.Join(tempDir, "input-pipe")
			outputPipePath := path.Join(tempDir, "output-pipe")

			require.Nil(t, syscall.Mkfifo(inputPipePath, 0644))
			require.Nil(t, syscall.Mkfifo(outputPipePath, 0644))

			writeCommand := exec.Command("buffer-proxy", "write", writeSasUri, "-i", inputPipePath)
			writeStdErr := bytes.NewBuffer(nil)
			writeCommand.Stderr = writeStdErr

			inputHash := make(chan []byte, 1)
			outputHash := make(chan []byte, 1)

			go func() {
				inputPipe, err := os.OpenFile(inputPipePath, os.O_WRONLY, 0644)
				require.Nil(t, err)
				defer inputPipe.Close()

				h := sha256.New()
				mw := io.MultiWriter(inputPipe, h)
				bufferproxy.Gen(int64(tc.size), mw)
				inputHash <- h.Sum(nil)
			}()

			readCommand := exec.Command("buffer-proxy", "read", readSasUri, "-o", outputPipePath)
			readStdErr := bytes.NewBuffer(nil)
			readCommand.Stderr = readStdErr

			go func() {
				outputPipe, err := os.OpenFile(outputPipePath, os.O_RDONLY, 0644)
				require.Nil(t, err)
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

func TestSizeParsing(t *testing.T) {
	t.Parallel()

	o1, err := exec.Command("buffer-proxy", "gen", "1K").Output()
	require.Nil(t, err)

	o2, err := exec.Command("buffer-proxy", "gen", "1KB").Output()
	require.Nil(t, err)

	o3, err := exec.Command("buffer-proxy", "gen", "1024").Output()
	require.Nil(t, err)

	require.Equal(t, o1, o2)
	require.Equal(t, o1, o3)
}

func TestMissingContainer(t *testing.T) {
	t.Parallel()

	bufferName := integrationtest.RunTygerSuceeds(t, "buffer", "create")
	readSasUri := integrationtest.RunTygerSuceeds(t, "buffer", "access", bufferName)
	readSasUri = strings.ReplaceAll(readSasUri, bufferName, bufferName+"missing")

	_, err := exec.Command("buffer-proxy", "read", readSasUri).Output()
	assert.NotNil(t, err)
	ee := err.(*exec.ExitError)
	errorString := string(ee.Stderr)

	assert.Contains(t, errorString, "Container validation failed")
}

func TestRunningFromPowershellRaisesWarning(t *testing.T) {
	t.Parallel()

	corruptionWarning := "PowerShell I/O redirection may corrupt binary data"

	cmd := exec.Command("pwsh", "-Command", "buffer-proxy gen 1")
	stdErrBuffer := bytes.NewBuffer(nil)
	cmd.Stderr = stdErrBuffer

	assert.Nil(t, cmd.Run())
	assert.Contains(t, stdErrBuffer.String(), corruptionWarning)

	cmd = exec.Command("buffer-proxy", "gen", "1")
	stdErrBuffer = bytes.NewBuffer(nil)
	cmd.Stderr = stdErrBuffer

	assert.Nil(t, cmd.Run())
	assert.NotContains(t, stdErrBuffer.String(), corruptionWarning)
}
