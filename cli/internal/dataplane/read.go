package dataplane

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
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

func Read(uri, proxyUri string, dop int, outputWriter io.Writer, readFunc BlobDownloadfunc) error {
	if readFunc == nil {
		readFunc = BlobDownload
	}

	ctx := log.With().Str("operation", "buffer read").Logger().WithContext(context.Background())
	httpClient, err := CreateHttpClient(proxyUri)
	if err != nil {
		return fmt.Errorf("failed to create http client: %w", err)
	}
	container, err := ValidateContainer(uri, httpClient)
	if err != nil {
		return fmt.Errorf("container validation failed: %w", err)
	}

	metrics := TransferMetrics{
		Context:   ctx,
		Container: container,
	}
	metrics.Start()

	// Read the blob meta data
	blobUri := container.GetNamedBlobUri(".bufferstart")
	respData, err := readFunc(ctx, NewClientWithLoggingContext(ctx, httpClient), blobUri, 0, nil)
	if err != nil {
		return fmt.Errorf("unable to read .bufferstart: %w", err)
	}

	var bufferFormat BufferFormat
	err = json.Unmarshal(respData.Data, &bufferFormat)
	if err != nil {
		return fmt.Errorf("unable to read .bufferstart: %w", err)
	} else if bufferFormat.Version != CurrentBufferVersion {
		return fmt.Errorf("unexected buffer version %s", bufferFormat.Version)
	}

	var nextBlobNumber int64 = 0
	var finalBlobNumber int64 = -1

	blobUri = container.GetNamedBlobUri(".bufferend")
	respData, err = readFunc(ctx, NewClientWithLoggingContext(ctx, httpClient), blobUri, -1, nil)

	responseChannel := make(chan chan BufferBlob, dop*2)
	var lock sync.Mutex

	if err == nil {
		var bufferFinalization BufferFinalization
		err = json.Unmarshal(respData.Data, &bufferFinalization)
		if err != nil {
			return fmt.Errorf("unable to read .bufferend: %w", err)
		} else if bufferFinalization.Status == "Failed" {
			return fmt.Errorf("the buffer status is set to failed")
		}
		atomic.StoreInt64(&finalBlobNumber, bufferFinalization.BlobCount)
	} else if err != ErrNotFound {
		return fmt.Errorf("unable to read .bufferend: %w", err)
	} else {
		go func() {
			for {
				blobUri = container.GetNamedBlobUri(".bufferend")
				respData, err = readFunc(ctx, NewClientWithLoggingContext(ctx, httpClient), blobUri, -1, nil)

				if err == nil {
					var bufferFinalization BufferFinalization
					json.Unmarshal(respData.Data, &bufferFinalization)
					if bufferFinalization.Status == "Failed" {
						log.Ctx(ctx).Error().Msg("buffer is invalid")

						c := make(chan BufferBlob, 5)
						responseChannel <- c
						c <- BufferBlob{BlobNumber: -1, LastError: errors.New("the buffer status is set to failed")}
						break
					}

					if atomic.LoadInt64(&finalBlobNumber) == -1 {
						atomic.StoreInt64(&finalBlobNumber, bufferFinalization.BlobCount)
					} else if atomic.LoadInt64(&finalBlobNumber) != bufferFinalization.BlobCount {
						log.Ctx(ctx).Error().Msg("blob count mismatch")

						c := make(chan BufferBlob, 5)
						responseChannel <- c
						c <- BufferBlob{BlobNumber: -1, LastError: errors.New("blob count mismatch")}

						break
					}

					log.Ctx(ctx).Trace().Msg(".bufferend read")

					break
				} else if err != ErrNotFound {
					log.Ctx(ctx).Err(err).Msg("unable to read .bufferend")

					c := make(chan BufferBlob, 5)
					responseChannel <- c
					c <- BufferBlob{BlobNumber: -1, LastError: err}
					break
				}

				time.Sleep(5 * time.Second)
			}
		}()
	}

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
				respData, err := readFunc(ctx, NewClientWithLoggingContext(ctx, httpClient), blobUri, blobNumber, &finalBlobNumber)
				if err != nil {
					if err != errPastEndOfBlob {
						log.Ctx(ctx).Err(err).Msg("Error downloading blob")
						c <- BufferBlob{BlobNumber: blobNumber, LastError: err}
					}
					break
				}
				metrics.Update(uint64(len(respData.Data)))

				md5Header := respData.Header.Get("Content-MD5")
				md5ChainHeader := respData.Header.Get("x-ms-meta-cumulative_md5_chain")

				calculatedMd5 := md5.Sum(respData.Data)

				md5Bytes, _ := base64.StdEncoding.DecodeString(md5Header)
				if !bytes.Equal(calculatedMd5[:], md5Bytes) {
					log.Ctx(ctx).Error().Msg("MD5 mismatch")
					c <- BufferBlob{BlobNumber: blobNumber, LastError: errors.New("MD5 mismatch")}
					break
				}

				c <- BufferBlob{BlobNumber: blobNumber, Contents: respData.Data, EncodedMD5Hash: md5Header, EncodedMD5ChainHash: md5ChainHeader, LastError: nil}
			}
		}()
	}

	lastTime := time.Now()
	var expcetedBlobNumber int64 = 0
	var encodedMD5HashChain string = EncodedMD5HashChainInitalValue
	for c := range responseChannel {
		blobResponse := <-c

		if blobResponse.LastError != nil {
			return blobResponse.LastError
		}

		if blobResponse.BlobNumber != expcetedBlobNumber {
			return fmt.Errorf("blob number returned out of sequence")
		}

		expcetedBlobNumber++

		if len(blobResponse.Contents) == 0 {
			break
		}

		if _, err := outputWriter.Write(blobResponse.Contents); err != nil {
			return fmt.Errorf("error writing to output: %w", err)
		}

		pool.Put(blobResponse.Contents)

		md5HashChain := md5.Sum([]byte(encodedMD5HashChain + blobResponse.EncodedMD5Hash))
		encodedMD5HashChain = base64.StdEncoding.EncodeToString(md5HashChain[:])

		if blobResponse.EncodedMD5ChainHash != encodedMD5HashChain {
			return fmt.Errorf("hash chain mismatch")
		}

		timeNow := time.Now()
		log.Ctx(ctx).Trace().Int64("blobNumber", blobResponse.BlobNumber).Dur("duration", timeNow.Sub(lastTime)).Msg("blob written to output")
		lastTime = timeNow
	}

	metrics.Stop()

	return nil
}

type BlobDownloadfunc func(ctx context.Context, httpClient *retryablehttp.Client, blobUri string, blobNumber int64, finalBlobNumber *int64) (*ReadBlobData, error)

func BlobDownload(ctx context.Context, httpClient *retryablehttp.Client, blobUri string, blobNumber int64, finalBlobNumber *int64) (*ReadBlobData, error) {
	// The last error that occurred relating to reading the body. retryablehttp does not retry when these happen
	// because reading the body happens after the call to HttpClient.Do()
	var lastBodyReadError *responseBodyReadError

	for retryCount := 0; ; retryCount++ {
		start := time.Now()

		if finalBlobNumber != nil {
			if num := atomic.LoadInt64(finalBlobNumber); num >= 0 && num < blobNumber {
				log.Ctx(ctx).Trace().Msg("Abandoning download after final blob")
				return nil, errPastEndOfBlob
			}
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

		respData, err := handleReadResponse(ctx, resp)

		if finalBlobNumber != nil && err == nil && resp.Header.Get("x-ms-meta-cumulative_md5_chain") == "" {
			err = &responseBodyReadError{reason: errors.New("expected x-ms-meta-cumulative_md5_chain header missing")}
		}

		if err == nil {
			log.Ctx(ctx).Trace().
				Int("contentLength", int(resp.ContentLength)).
				Dur("duration", time.Since(start)).
				Msg("Downloaded blob")

			if len(respData.Data) == 0 && finalBlobNumber != nil {
				num := atomic.LoadInt64(finalBlobNumber)
				if num == -1 {
					atomic.StoreInt64(finalBlobNumber, blobNumber)
				} else if num != blobNumber {
					log.Ctx(ctx).Fatal().Msg("blob count mismatch")
				}
			}

			return respData, nil
		}
		if err == ErrNotFound {
			if blobNumber == -1 {
				return nil, err
			}

			log.Ctx(ctx).Trace().Int64("blobnumber", blobNumber).Msg("Waiting for blob")
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

type ReadBlobData struct {
	Data   []byte
	Header http.Header
}

func handleReadResponse(ctx context.Context, resp *http.Response) (*ReadBlobData, error) {
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

		if resp.Header.Get("Content-MD5") == "" {
			return nil, &responseBodyReadError{reason: errors.New("expected Content-MD5 header missing")}
		}

		response := ReadBlobData{Data: buf, Header: resp.Header}

		return &response, nil
	case http.StatusNotFound:
		io.Copy(io.Discard, resp.Body)
		return nil, ErrNotFound
	default:
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(bodyBytes))
	}
}
