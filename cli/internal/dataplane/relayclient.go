// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package dataplane

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"syscall"
	"time"

	"github.com/microsoft/tyger/cli/internal/client"
	"github.com/rs/zerolog/log"
)

func relayWrite(ctx context.Context, containerClient *ContainerClient, connectionType client.TygerConnectionType, inputReader io.Reader) error {
	if err := pingRelay(ctx, containerClient, connectionType); err != nil {
		return err
	}

	httpClient := client.CloneRetryableClient(containerClient.innerClient)
	httpClient.HTTPClient.Timeout = 0

	metrics := NewTransferMetrics(ctx)

	pipeReader, pipeWriter := io.Pipe()

	originalInputReader := inputReader
	go func() {
		err := copyToPipe(pipeWriter, originalInputReader)
		pipeWriter.CloseWithError(err)
	}()

	inputReader = pipeReader
	inputReader = &ReaderWithMetrics{transferMetrics: metrics, reader: inputReader}

	partiallyBufferedReader := NewPartiallyBufferedReader(inputReader, 64*1024)

	req := containerClient.NewNonRetryableRequestWithRelativeUrl(ctx, http.MethodPut, "", partiallyBufferedReader)

	for retryCount := 0; ; retryCount++ {
		containerClient.updateRequestUrl(req)
		resp, err := httpClient.HTTPClient.Do(req)
		if err != nil {
			if rewindErr := partiallyBufferedReader.Rewind(); rewindErr != nil || retryCount > 10 {
				return fmt.Errorf("error writing to relay: %w", client.RedactHttpError(err))
			} else {
				log.Ctx(ctx).Warn().Err(err).Msg("retryable error writing to relay")
				time.Sleep(time.Second)
				continue
			}
		}

		io.Copy(io.Discard, resp.Body)
		if resp.StatusCode != http.StatusAccepted {
			if resp.StatusCode == http.StatusMethodNotAllowed {
				return fmt.Errorf("the buffer is an output buffer and cannot be read from")
			}

			err := relayErrorCodeToErr(resp.Header.Get(ErrorCodeHeader))
			if err != nil {
				return fmt.Errorf("error writing to relay: %w", err)
			}

			return fmt.Errorf("error writing to relay: %s", resp.Status)
		}

		metrics.Stop()
		return err
	}
}

func readRelay(ctx context.Context, containerClient *ContainerClient, connectionType client.TygerConnectionType, outputWriter io.Writer) error {
	if err := pingRelay(ctx, containerClient, connectionType); err != nil {
		return err
	}

	httpClient := client.CloneRetryableClient(containerClient.innerClient)
	httpClient.HTTPClient.Timeout = 0

	req := containerClient.NewRequestWithRelativeUrl(ctx, http.MethodGet, "", nil)
	containerClient.updateRequestUrl(req.Request)

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("error reading from relay: %w", client.RedactHttpError(err))
	}

	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		if resp.StatusCode == http.StatusMethodNotAllowed {
			return fmt.Errorf("the buffer is an input buffer and cannot be written to")
		}

		err := relayErrorCodeToErr(resp.Header.Get(ErrorCodeHeader))
		if err != nil {
			return fmt.Errorf("error reading from relay: %w", err)
		}

		return fmt.Errorf("error reading from relay: %s", resp.Status)
	}

	metrics := NewTransferMetrics(ctx)

	_, err = io.Copy(outputWriter, &ReaderWithMetrics{transferMetrics: metrics, reader: resp.Body})

	trailerErrorCode := resp.Trailer.Get(ErrorCodeHeader)

	if err == nil && trailerErrorCode != "" {
		err = relayErrorCodeToErr(trailerErrorCode)
	}

	if err != nil {
		err = fmt.Errorf("error reading from relay: %w", err)
	} else {
		metrics.Stop()
	}

	return client.RedactHttpError(err)
}

func relayErrorCodeToErr(errorCode string) error {
	switch errorCode {
	case "":
		return nil
	case alreadyCalledErrorCode:
		return errors.New("the buffer endpoint can only be called once")
	case failedToOpenReaderErrorCode:
		return errors.New("failed to open reader")
	case contextCancelledErrorCode:
		return errors.New("context cancelled")
	default:
		return errors.New(errorCode)
	}
}

func pingRelay(ctx context.Context, containerClient *ContainerClient, connectionType client.TygerConnectionType) error {
	log.Ctx(ctx).Info().Msg("Attempting to connect to relay server...")

	headRequest := containerClient.NewNonRetryableRequestWithRelativeUrl(ctx, http.MethodHead, "", nil)

	// don't use retryable client here so that we can do special error handling
	unknownErrCount := 0
	for retryCount := 0; ; retryCount++ {
		containerClient.updateRequestUrl(headRequest)
		resp, err := containerClient.innerClient.HTTPClient.Do(headRequest)
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				log.Ctx(ctx).Info().Msg("Connection to relay server established.")

				return nil
			}

			if resp.StatusCode == http.StatusBadGateway && (connectionType == client.TygerConnectionTypeDocker || connectionType == client.TygerConnectionTypeSsh) {
				// stdio-proxy returns this status code for connection errors to the underlying service
				errorHeader := resp.Header.Get("x-ms-error")
				switch errorHeader {
				case "ENOENT":
					return fmt.Errorf("buffer relay server does not exist")
				case "ECONNREFUSED":
					log.Ctx(ctx).Debug().Msg("Waiting for relay server to be ready.")
				default:
					return fmt.Errorf("error connecting to relay server: %s: %s", resp.Status, errorHeader)
				}
			}
		} else {
			if errors.Is(err, ctx.Err()) {
				return err
			}
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOENT) {
				return fmt.Errorf("buffer relay server does not exist: %w", client.RedactHttpError(err))
			}
			if errors.Is(err, syscall.ECONNREFUSED) {
				log.Ctx(ctx).Debug().Msg("Waiting for relay server to be ready.")
			} else {
				unknownErrCount++
				if unknownErrCount > 10 {
					return fmt.Errorf("error connecting to relay server: %w", client.RedactHttpError(err))
				}

				log.Ctx(ctx).Warn().Err(err).Msg("retryable error connecting to relay server")
			}
		}

		switch {
		case retryCount < 10:
			time.Sleep(100 * time.Millisecond)
		case retryCount < 100:
			time.Sleep(500 * time.Millisecond)
		case retryCount < 1000:
			time.Sleep(1 * time.Second)
		default:
			time.Sleep(5 * time.Second)
		}
	}
}

type ReaderWithMetrics struct {
	transferMetrics *TransferMetrics
	reader          io.Reader
}

func (c *ReaderWithMetrics) Read(p []byte) (n int, err error) {
	n, err = c.reader.Read(p)
	if n > 0 {
		c.transferMetrics.EnsureStarted(nil)
		c.transferMetrics.UpdateCompleted(uint64(n), 0)
		c.transferMetrics.UpdateInFlight(uint64(n))
	}
	return n, err
}

// An io.Reader that stores the first N bytes read from the underlying reader as they
// are read so that it can be rewound and read again, if <= N bytes were read.
type PartiallyBufferedReader struct {
	io.Reader
	buffer            []byte
	returnFirstBuffer []byte
}

func NewPartiallyBufferedReader(r io.Reader, capacity int) *PartiallyBufferedReader {
	buf := make([]byte, 0, capacity)
	return &PartiallyBufferedReader{
		Reader: r,
		buffer: buf,
	}
}

func (r *PartiallyBufferedReader) Read(p []byte) (n int, err error) {
	if len(r.returnFirstBuffer) != 0 {
		n = min(len(p), len(r.returnFirstBuffer))
		copy(p, r.returnFirstBuffer[:n])
		r.returnFirstBuffer = r.returnFirstBuffer[n:]

		return n, nil
	}

	n, err = r.Reader.Read(p)

	if r.buffer == nil {
		return n, err
	}

	if len(r.buffer)+n <= cap(r.buffer) {
		r.buffer = r.buffer[:len(r.buffer)+n]
		copy(r.buffer[len(r.buffer)-n:], p[:n])
	} else {
		r.buffer = nil
	}

	return n, err
}

func (r *PartiallyBufferedReader) Rewind() error {
	if r.buffer == nil {
		return errors.New("cannot rewind reader")
	}

	r.returnFirstBuffer = r.buffer

	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
