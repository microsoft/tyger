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

	"github.com/hashicorp/go-retryablehttp"
	"github.com/microsoft/tyger/cli/internal/client"
	"github.com/rs/zerolog/log"
)

func relayWrite(ctx context.Context, httpClient *retryablehttp.Client, container *Container, inputReader io.Reader) error {
	containerUri := container.String()
	if err := pingRelay(ctx, containerUri, httpClient); err != nil {
		return err
	}

	relayClient := *httpClient.HTTPClient
	relayClient.Timeout = 0

	metrics := TransferMetrics{
		Context:   ctx,
		Container: container,
	}

	metrics.Start()

	request, err := http.NewRequestWithContext(ctx, http.MethodPut, containerUri, &ReaderWithMetrics{transferMetrics: &metrics, reader: inputReader})
	if err != nil {
		return fmt.Errorf("error creating request: %w", err)
	}

	resp, err := relayClient.Do(request)
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

func readRelay(ctx context.Context, httpClient *retryablehttp.Client, container *Container, outputWriter io.Writer) error {
	containerUri := container.String()
	if err := pingRelay(ctx, containerUri, httpClient); err != nil {
		return err
	}

	relayClient := *httpClient.HTTPClient
	relayClient.Timeout = 0

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, containerUri, nil)
	if err != nil {
		return fmt.Errorf("error creating request: %w", err)
	}

	resp, err := relayClient.Do(request)
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

func pingRelay(ctx context.Context, uri string, httpClient *retryablehttp.Client) error {
	log.Ctx(ctx).Info().Msg("Attempting to connect to relay server...")
	headRequest, err := http.NewRequestWithContext(ctx, http.MethodHead, uri, nil)
	if err != nil {
		return fmt.Errorf("error creating request: %w", err)
	}

	for retryCount := 0; ; retryCount++ {
		resp, err := httpClient.HTTPClient.Do(headRequest)
		if err == nil && resp.StatusCode == http.StatusOK {
			log.Ctx(ctx).Info().Msg("Connection to relay server established.")
			return nil
		}

		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("buffer relay server does not exist: %w", err)
			}

			if errors.Is(err, syscall.ECONNREFUSED) {
				log.Ctx(ctx).Debug().Msg("Waiting for relay server to be ready.")
			} else {
				return fmt.Errorf("error connecting to relay server: %w", client.RedactHttpError(err))
			}
		} else {
			log.Ctx(ctx).Debug().Int("status", resp.StatusCode).Msg("Waiting for relay server to be ready.")
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
