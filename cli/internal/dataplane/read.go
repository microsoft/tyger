package dataplane

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/go-retryablehttp"
	pool "github.com/libp2p/go-buffer-pool"
	"github.com/rs/zerolog/log"
)

const (
	DefaultReadDop = 32
)

var (
	errPastEndOfBlob = errors.New("past end of blob")
)

func Read(uri, proxyUri string, dop int, outputWriter io.Writer) {
	ctx := log.With().Str("operation", "buffer read").Logger().WithContext(context.Background())
	httpClient, err := CreateHttpClient(proxyUri)
	if err != nil {
		log.Ctx(ctx).Fatal().Err(err).Msg("Failed to create http client")
	}
	container, err := ValidateContainer(uri, httpClient)
	if err != nil {
		log.Ctx(ctx).Fatal().Err(err).Msg("Container validation failed")
	}

	metrics := TransferMetrics{
		Context:   ctx,
		Container: container,
	}
	metrics.Start()

	responseChannel := make(chan chan BufferBlob, dop*2)
	var lock sync.Mutex
	var nextBlobNumber int64 = 0
	var finalBlobNumber int64 = -1

	for i := 0; i < dop; i++ {
		go func() {
			c := make(chan BufferBlob, 5)
			for {
				lock.Lock()
				blobNumber := nextBlobNumber
				nextBlobNumber++
				responseChannel <- c
				lock.Unlock()

				blobUri := container.GetBlobUri(blobNumber)
				ctx := log.Ctx(ctx).With().Int64("blobNumber", blobNumber).Logger().WithContext(ctx)
				bytes, err := WaitForBlobAndDownload(ctx, NewClientWithLoggingContext(ctx, httpClient), blobUri, blobNumber, &finalBlobNumber)
				if err != nil {
					if err == errPastEndOfBlob {
						break
					}
					log.Ctx(ctx).Fatal().Err(err).Msg("Error downloading blob")
				}
				metrics.Update(uint64(len(bytes)))
				c <- BufferBlob{BlobNumber: blobNumber, Contents: bytes}
			}
		}()
	}

	lastTime := time.Now()
	for c := range responseChannel {
		blobResponse := <-c

		if len(blobResponse.Contents) == 0 {
			break
		}

		if _, err := outputWriter.Write(blobResponse.Contents); err != nil {
			log.Ctx(ctx).Fatal().Err(err).Msg("Error writing to output")
		}

		pool.Put(blobResponse.Contents)

		timeNow := time.Now()
		log.Ctx(ctx).Trace().Int64("blobNumber", blobResponse.BlobNumber).Dur("duration", timeNow.Sub(lastTime)).Msg("blob written to output")
		lastTime = timeNow
	}

	metrics.Stop()
}

func WaitForBlobAndDownload(ctx context.Context, httpClient *retryablehttp.Client, blobUri string, blobNumber int64, finalBlobNumber *int64) ([]byte, error) {
	// The last error that occurred relating to reading the body. retryablehttp does not retry when these happen
	// because reading the body happens after the call to HttpClient.Do()
	var lastBodyReadError *responseBodyReadError

	for retryCount := 0; ; retryCount++ {
		start := time.Now()

		if num := atomic.LoadInt64(finalBlobNumber); num >= 0 && num < blobNumber {
			log.Ctx(ctx).Trace().Msg("Abandoning download after final blob")
			return nil, errPastEndOfBlob
		}

		req, err := retryablehttp.NewRequest(http.MethodGet, blobUri, nil)
		if err != nil {
			return nil, err
		}

		AddCommonBlobRequestHeaders(req.Header)

		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, RedactHttpError(err)
		}

		data, err := handleReadResponse(resp)
		if err == nil {
			log.Ctx(ctx).Trace().
				Int("contentLength", int(resp.ContentLength)).
				Dur("duration", time.Since(start)).
				Msg("Downloaded blob")

			if len(data) == 0 {
				atomic.StoreInt64(finalBlobNumber, blobNumber)
			}

			return data, nil
		}
		if err == ErrNotFound {
			log.Ctx(ctx).Trace().Msg("Waiting for blob")
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()

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

			continue
		}
		if err, ok := err.(*responseBodyReadError); ok {
			if lastBodyReadError == nil {
				lastBodyReadError = err
				retryCount = 0
			}

			if retryCount < MaxRetries {
				log.Ctx(ctx).Warn().Err(err).Msg("Error reading response body, retrying")

				// wait in the same way as retryablehttp
				wait := httpClient.Backoff(httpClient.RetryWaitMin, httpClient.RetryWaitMax, retryCount, resp)
				timer := time.NewTimer(wait)
				select {
				case <-req.Context().Done():
					timer.Stop()
					httpClient.HTTPClient.CloseIdleConnections()
					return nil, req.Context().Err()
				case <-timer.C:
				}

				continue
			}
		}

		return nil, RedactHttpError(err)
	}
}

func handleReadResponse(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		if resp.ContentLength < 0 {
			io.Copy(io.Discard, resp.Body)
			return nil, &responseBodyReadError{reason: errors.New("expected Content-Length header missing")}
		}

		buf := pool.Get(int(resp.ContentLength))

		_, err := io.ReadFull(resp.Body, buf)
		if err != nil {
			// return the buffer to the pool
			pool.Put(buf)
			return nil, &responseBodyReadError{reason: err}
		}

		calculatedMd5 := md5.Sum(buf)

		md5Header := resp.Header.Get("Content-MD5")
		if md5Header == "" {
			return nil, &responseBodyReadError{reason: errors.New("expected Content-MD5 header missing")}
		}

		md5Bytes, _ := base64.StdEncoding.DecodeString(md5Header)
		if !bytes.Equal(calculatedMd5[:], md5Bytes) {
			pool.Put(buf)
			return nil, &responseBodyReadError{reason: errMd5Mismatch}
		}

		return buf, nil
	case http.StatusNotFound:
		io.Copy(io.Discard, resp.Body)
		return nil, ErrNotFound
	default:
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(bodyBytes))
	}
}
