// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package dataplane

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"syscall"
	"time"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/microsoft/tyger/cli/internal/client"
	"github.com/rs/zerolog/log"
)

func relayWrite(ctx context.Context, httpClient *retryablehttp.Client, connectionType client.TygerConnectionType, container *Container, inputReader io.Reader) error {
	var err error
	container, err = pingRelay(ctx, container, httpClient, connectionType)
	if err != nil {
		return err
	}

	httpClient = client.CloneRetryableClient(httpClient)
	httpClient.HTTPClient.Timeout = 0

	metrics := TransferMetrics{
		Context:   ctx,
		Container: container,
	}

	pipeReader, pipeWriter := io.Pipe()

	originalInputReader := inputReader
	go func() {
		err := copyToPipe(pipeWriter, originalInputReader)
		pipeWriter.CloseWithError(err)
	}()

	inputReader = pipeReader

	metrics.Start()

	request, err := http.NewRequestWithContext(ctx, http.MethodPut, container.String(), &ReaderWithMetrics{transferMetrics: &metrics, reader: inputReader})
	if err != nil {
		return fmt.Errorf("error creating request: %w", err)
	}

	resp, err := httpClient.HTTPClient.Do(request)
	if err != nil {
		return fmt.Errorf("error writing to relay: %w", client.RedactHttpError(err))
	}
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("error writing to relay: %s", resp.Status)
	}

	metrics.Stop()
	return err
}

func readRelay(ctx context.Context, httpClient *retryablehttp.Client, connectionType client.TygerConnectionType, container *Container, outputWriter io.Writer) error {
	var err error
	container, err = pingRelay(ctx, container, httpClient, connectionType)
	if err != nil {
		return err
	}

	httpClient = client.CloneRetryableClient(httpClient)
	httpClient.HTTPClient.Timeout = 0

	request, err := retryablehttp.NewRequestWithContext(ctx, http.MethodGet, container.String(), nil)
	if err != nil {
		return fmt.Errorf("error creating request: %w", err)
	}

	resp, err := httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("error reading from relay: %w", client.RedactHttpError(err))
	}

	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("error reading from relay: %s", resp.Status)
	}

	metrics := TransferMetrics{
		Context:   ctx,
		Container: container,
	}

	metrics.Start()

	_, err = io.Copy(outputWriter, &ReaderWithMetrics{transferMetrics: &metrics, reader: resp.Body})

	if err == nil {
		metrics.Stop()
	}

	return client.RedactHttpError(err)
}

func pingRelay(ctx context.Context, containerUrl *Container, httpClient *retryablehttp.Client, connectionType client.TygerConnectionType) (*Container, error) {
	log.Ctx(ctx).Info().Msg("Attempting to connect to relay server...")
	headRequest, err := http.NewRequestWithContext(ctx, http.MethodHead, containerUrl.String(), nil)
	if err != nil {
		return containerUrl, fmt.Errorf("error creating request: %w", err)
	}

	for retryCount := 0; ; retryCount++ {
		resp, err := httpClient.HTTPClient.Do(headRequest)
		if err == nil {
			if resp.StatusCode == http.StatusOK {
				log.Ctx(ctx).Info().Msg("Connection to relay server established.")

				secondaryEndpoint := resp.Header.Get("x-ms-secondary-endpoint")
				if secondaryEndpoint != "" && connectionType == client.TygerConnectionTypeDocker {
					secondaryUrl, err := url.Parse(secondaryEndpoint)
					if err != nil {
						return containerUrl, fmt.Errorf("error parsing secondary endpoint: %w", err)
					}

					log.Info().Msg("Upgrading to secondary relay server endpoint for improved performance.")

					secondaryUrl.RawQuery = containerUrl.RawQuery
					return &Container{URL: secondaryUrl}, nil
				}

				return containerUrl, nil
			}

			if resp.StatusCode == http.StatusBadGateway && (connectionType == client.TygerConnectionTypeDocker || connectionType == client.TygerConnectionTypeSsh) {
				// stdio-proxy returns this status code for connection errors to the underlying service
				errorHeader := resp.Header.Get("x-ms-error")
				switch errorHeader {
				case "ENOENT":
					return containerUrl, fmt.Errorf("buffer relay server does not exist")
				case "ECONNREFUSED":
					log.Ctx(ctx).Debug().Msg("Waiting for relay server to be ready.")
				default:
					return containerUrl, fmt.Errorf("error connecting to relay server: %s: %s", resp.Status, errorHeader)
				}
			}

			log.Ctx(ctx).Debug().Int("status", resp.StatusCode).Msg("Waiting for relay server to be ready.")
		} else {
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOENT) {
				return containerUrl, fmt.Errorf("buffer relay server does not exist: %w", client.RedactHttpError(err))
			}

			if errors.Is(err, syscall.ECONNREFUSED) {
				log.Ctx(ctx).Debug().Msg("Waiting for relay server to be ready.")
			} else {
				return containerUrl, fmt.Errorf("error connecting to relay server: %w", client.RedactHttpError(err))
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
		c.transferMetrics.Update(uint64(n))
	}
	return n, err
}
