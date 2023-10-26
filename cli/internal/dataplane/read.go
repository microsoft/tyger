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
	errPastEndOfBlob     = errors.New("past end of blob")
	ErrNotFound          = errors.New("not found")
	errBufferFailedState = errors.New("the buffer is in a permanently failed state")
)

type readOptions struct {
	dop        int
	httpClient *retryablehttp.Client
}

type ReadOption func(o *readOptions)

func WithReadDop(dop int) ReadOption {
	return func(o *readOptions) {
		o.dop = dop
	}
}

func WithReadHttpClient(httpClient *retryablehttp.Client) ReadOption {
	return func(o *readOptions) {
		o.httpClient = httpClient
	}
}

func Read(ctx context.Context, uri string, outputWriter io.Writer, options ...ReadOption) error {
	readOptions := &readOptions{
		dop: DefaultReadDop,
	}
	for _, o := range options {
		o(readOptions)
	}

	if readOptions.httpClient == nil {
		var err error
		readOptions.httpClient, err = CreateHttpClient(ctx, "")
		if err != nil {
			return fmt.Errorf("failed to create http client: %w", err)
		}
	}

	httpClient := readOptions.httpClient

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	ctx = log.With().Str("operation", "buffer read").Logger().WithContext(ctx)
	container, err := NewContainer(uri, httpClient)
	if err != nil {
		log.Ctx(ctx).Fatal().Err(err).Msg("invalid URL:")
	}

	if err := readBufferStart(ctx, httpClient, container); err != nil {
		return err
	}

	errorChannel := make(chan error, 1)
	waitForBlobs := true

	go func() {
		err := pollForBufferEnd(ctx, httpClient, container)
		if err != nil {
			errorChannel <- err
			return
		}
		// All blobs should have been written by now.
		// If a blob is missing, it probably has been deleted.
		// We'll wait in case any reads just failed and should be restarted,
		// but we can now disable waiting for blobs.
		time.Sleep(2 * time.Second)
		waitForBlobs = false
	}()

	metrics := TransferMetrics{
		Context:   ctx,
		Container: container,
	}
	metrics.Start()

	responseChannel := make(chan chan BufferBlob, readOptions.dop*2)
	var lock sync.Mutex
	var nextBlobNumber int64 = 0
	var finalBlobNumber int64 = -1

	for i := 0; i < readOptions.dop; i++ {
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
				respData, err := DownloadBlob(ctx, NewClientWithLoggingContext(ctx, httpClient), blobUri, &waitForBlobs, &blobNumber, &finalBlobNumber)
				if err != nil {
					if err == errPastEndOfBlob {
						break
					}
					errorChannel <- fmt.Errorf("error downloading blob: %w", err)
					return
				}
				metrics.Update(uint64(len(respData.Data)))

				md5Header := respData.Header.Get(ContentMD5Header)
				if md5Header == "" {
					panic("Content-MD5 header missing. This should have already been checked")
				}

				md5ChainHeader := respData.Header.Get(HashChainHeader)
				if md5ChainHeader == "" {
					errorChannel <- &responseBodyReadError{reason: fmt.Errorf("expected %s header missing", HashChainHeader)}
					return
				}

				c <- BufferBlob{BlobNumber: blobNumber, Contents: respData.Data, EncodedMD5Hash: md5Header, EncodedMD5ChainHash: md5ChainHeader}
			}
		}()
	}

	doneChan := make(chan any)
	go func() {
		lastTime := time.Now()
		var expcetedBlobNumber int64 = 0
		var encodedMD5HashChain string = EncodedMD5HashChainInitalValue
		for c := range responseChannel {
			blobResponse := <-c

			if blobResponse.BlobNumber != expcetedBlobNumber {
				errorChannel <- fmt.Errorf("blob number returned out of sequence. Expected %d, got %d", expcetedBlobNumber, blobResponse.BlobNumber)
				return
			}

			expcetedBlobNumber++

			if len(blobResponse.Contents) == 0 {
				break
			}

			if _, err := outputWriter.Write(blobResponse.Contents); err != nil {
				errorChannel <- fmt.Errorf("error writing to output: %w", err)
				return
			}

			pool.Put(blobResponse.Contents)

			md5HashChain := md5.Sum([]byte(encodedMD5HashChain + blobResponse.EncodedMD5Hash))
			encodedMD5HashChain = base64.StdEncoding.EncodeToString(md5HashChain[:])

			if blobResponse.EncodedMD5ChainHash != encodedMD5HashChain {
				errorChannel <- errors.New("hash chain mismatch")
				return
			}

			timeNow := time.Now()
			log.Ctx(ctx).Trace().Int64("blobNumber", blobResponse.BlobNumber).Dur("duration", timeNow.Sub(lastTime)).Msg("blob written to output")
			lastTime = timeNow
		}

		close(doneChan)
	}()

	select {
	case <-doneChan:
		metrics.Stop()
		return nil
	case err := <-errorChannel:
		return err
	}
}

func readBufferStart(ctx context.Context, httpClient *retryablehttp.Client, container *Container) error {
	wait := true
	data, err := DownloadBlob(ctx, httpClient, container.GetStartMetadataUri(), &wait, nil, nil)
	if err != nil {
		return err
	}
	bufferStartMetadata := BufferStartMetadata{}
	if err := json.Unmarshal(data.Data, &bufferStartMetadata); err != nil {
		return fmt.Errorf("failed to unmarshal buffer start metadata: %w", err)
	}
	if bufferStartMetadata.Version != CurrentBufferFormatVersion {
		return fmt.Errorf("unsupported format buffer version '%s'. Expected '%s", bufferStartMetadata.Version, CurrentBufferFormatVersion)
	}

	return nil
}

func pollForBufferEnd(ctx context.Context, httpClient *retryablehttp.Client, container *Container) error {
	for ctx.Err() == nil {
		wait := false
		data, err := DownloadBlob(ctx, httpClient, container.GetEndMetadataUri(), &wait, nil, nil)
		if err != nil {
			if err == ErrNotFound {
				time.Sleep(5 * time.Second)
				continue
			}
			return err
		}
		bufferEndMetadata := BufferEndMetadata{}
		if err := json.Unmarshal(data.Data, &bufferEndMetadata); err != nil {
			return fmt.Errorf("failed to unmarshal buffer end metadata: %w", err)
		}

		switch bufferEndMetadata.Status {
		case BufferStatusComplete:
			return nil
		case BufferStatusFailed:
			return errBufferFailedState
		default:
			log.Warn().Msgf("Buffer end blob has unexpected status '%s'", bufferEndMetadata.Status)
			return nil
		}
	}

	return nil
}

func DownloadBlob(ctx context.Context, httpClient *retryablehttp.Client, blobUri string, waitForBlob *bool, blobNumber *int64, finalBlobNumber *int64) (*readData, error) {
	// The last error that occurred relating to reading the body. retryablehttp does not retry when these happen
	// because reading the body happens after the call to HttpClient.Do()
	var lastBodyReadError *responseBodyReadError

	for retryCount := 0; ; retryCount++ {
		start := time.Now()

		if blobNumber != nil {
			if num := atomic.LoadInt64(finalBlobNumber); num >= 0 && num < *blobNumber {
				log.Ctx(ctx).Trace().Msg("Abandoning download after final blob")
				return nil, errPastEndOfBlob
			}
		}

		req, err := retryablehttp.NewRequest(http.MethodGet, blobUri, nil)
		if err != nil {
			return nil, err
		}

		req = req.WithContext(ctx)

		AddCommonBlobRequestHeaders(req.Header)

		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, RedactHttpError(err)
		}

		respData, err := handleReadResponse(ctx, resp)
		if err == nil {
			log.Ctx(ctx).Trace().
				Int("contentLength", int(resp.ContentLength)).
				Dur("duration", time.Since(start)).
				Msg("Downloaded blob")

			if blobNumber != nil && len(respData.Data) == 0 {
				atomic.StoreInt64(finalBlobNumber, *blobNumber)
			}

			return respData, nil
		}
		if err == errMd5Mismatch {
			if retryCount < 5 {
				log.Ctx(ctx).Debug().Msg("MD5 mismatch, retrying")
				continue
			} else {
				return nil, fmt.Errorf("failed to read blob: %w", RedactHttpError(err))
			}
		}
		if *waitForBlob && err == ErrNotFound {
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
		if errors.Is(err, ctx.Err()) {
			// the context has been canceled
			return nil, err
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

type readData struct {
	Data   []byte
	Header http.Header
}

func handleReadResponse(ctx context.Context, resp *http.Response) (*readData, error) {
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
		md5Header := resp.Header.Get(ContentMD5Header)
		if md5Header == "" {
			pool.Put(buf)
			return nil, errors.New("expected Content-MD5 header missing")
		}

		md5Bytes, _ := base64.StdEncoding.DecodeString(md5Header)
		if !bytes.Equal(calculatedMd5[:], md5Bytes) {
			pool.Put(buf)
			return nil, errMd5Mismatch
		}

		response := readData{Data: buf, Header: resp.Header}

		return &response, nil
	case http.StatusNotFound:
		io.Copy(io.Discard, resp.Body)
		switch resp.Header.Get("x-ms-error-code") {
		case "BlobNotFound":
			return nil, ErrNotFound
		case "ContainerNotFound":
			return nil, errBufferDoesNotExist
		}
		fallthrough
	default:
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(bodyBytes))
	}
}
