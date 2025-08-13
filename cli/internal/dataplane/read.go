// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package dataplane

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/go-retryablehttp"
	pool "github.com/libp2p/go-buffer-pool"
	"github.com/microsoft/tyger/cli/internal/client"
	"github.com/microsoft/tyger/cli/internal/controlplane"
	"github.com/rs/zerolog/log"
)

const (
	DefaultReadDop  = 32
	MaxRetries      = 6
	ResponseTimeout = 100 * time.Second
)

var (
	errPastEndOfBlob     = errors.New("past end of blob")
	ErrNotFound          = errors.New("not found")
	ErrBufferFailedState = errors.New("the buffer is in a permanently failed state")
)

type readOptions struct {
	dop            int
	httpClient     *retryablehttp.Client
	connectionType client.TygerConnectionType
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

func Read(ctx context.Context, container *Container, outputWriter io.Writer, options ...ReadOption) error {
	readOptions := &readOptions{
		dop: DefaultReadDop,
	}
	for _, o := range options {
		o(readOptions)
	}

	ctx = log.Ctx(ctx).With().
		Str("operation", "buffer read").
		Str("buffer", container.GetContainerName()).
		Logger().WithContext(ctx)

	if readOptions.httpClient == nil {
		tygerClient, _ := controlplane.GetClientFromCache()
		if tygerClient != nil {
			readOptions.connectionType = tygerClient.ConnectionType()
			readOptions.httpClient = tygerClient.DataPlaneClient.Client
			if tygerClient.ConnectionType() == client.TygerConnectionTypeSsh && container.Scheme() == "http+unix" && !container.SupportsRelay() {
				httpClient, tunnelPool, err := createSshTunnelPoolClient(ctx, tygerClient, container, readOptions.dop)
				if err != nil {
					return err
				}

				defer tunnelPool.Close()
				readOptions.httpClient = httpClient
			}
		} else {
			readOptions.httpClient = client.DefaultRetryableClient
		}
	}

	containerClient := container.NewContainerClient(readOptions.httpClient)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	if container.SupportsRelay() {
		return readRelay(ctx, containerClient, readOptions.connectionType, outputWriter)
	}

	errorChannel := make(chan error, readOptions.dop*2)
	waitForBlobs := atomic.Bool{}
	waitForBlobs.Store(true)

	go func() {
		err := pollForBufferEnd(ctx, containerClient)
		if err != nil {
			errorChannel <- err
			cancel()
			return
		}
		// All blobs should have been written successfully by now.
		waitForBlobs.Store(false)
	}()

	if err := readBufferStart(ctx, containerClient); err != nil {
		if errors.Is(err, ctx.Err()) {
			select {
			case errorChannelResult := <-errorChannel:
				return errorChannelResult
			default:
			}
		}
		return err
	}

	metrics := NewTransferMetrics(ctx)

	responseChannel := make(chan chan BufferBlob, readOptions.dop*2)
	var lock sync.Mutex
	var nextBlobNumber int64 = 0

	finalBlobNumber := atomic.Int64{}
	finalBlobNumber.Store(-1)

	wg := sync.WaitGroup{}
	for i := 0; i < readOptions.dop; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := make(chan BufferBlob, 5)
			firstBlobForThisGoroutine := true
			for {
				lock.Lock()
				blobNumber := nextBlobNumber
				nextBlobNumber++
				select {
				case responseChannel <- c:
				case <-ctx.Done():
					lock.Unlock()
					errorChannel <- ctx.Err()
					return
				}

				lock.Unlock()

				ctx := log.Ctx(ctx).With().Int64("blobNumber", blobNumber).Logger().WithContext(ctx)
				respData, requestStartTime, err := DownloadBlob(ctx, metrics, containerClient, MakeBlobPath(blobNumber), &waitForBlobs, &blobNumber, &finalBlobNumber)
				if err != nil {
					if err == errPastEndOfBlob {
						break
					}
					if err == ErrNotFound {
						// This error will most likely not be surfaced. We are adding it to the channel in case this buffer is not
						// past the final blob.
						c <- BufferBlob{BlobNumber: blobNumber, Error: fmt.Errorf("blob number %d was expected to exist but does not", blobNumber)}
						break
					}
					errorChannel <- fmt.Errorf("error downloading blob: %w", err)
					return
				}

				if firstBlobForThisGoroutine {
					metrics.EnsureStarted(requestStartTime)
					firstBlobForThisGoroutine = false
				}

				metrics.UpdateCompleted(uint64(len(respData.Data)), 0)

				md5Header := respData.Header.Get(ContentMD5Header)
				if md5Header == "" {
					panic("Content-MD5 header missing. This should have already been checked")
				}

				hashChainHeader := respData.Header.Get(HashChainHeader)
				if hashChainHeader == "" {
					errorChannel <- &responseBodyReadError{reason: fmt.Errorf("expected %s header missing", HashChainHeader)}
					return
				}

				c <- BufferBlob{BlobNumber: blobNumber, Contents: respData.Data, EncodedMD5Hash: md5Header, EncodedMD5ChainHash: hashChainHeader}
			}
		}()
	}

	doneChan := make(chan any)
	go func() {
		lastTime := time.Now()
		var expectedBlobNumber int64 = 0
		var encodedHashChain string = EncodedHashChainInitialValue
		for c := range responseChannel {
			blobResponse := <-c

			if blobResponse.BlobNumber != expectedBlobNumber {
				errorChannel <- fmt.Errorf("blob number returned out of sequence. Expected %d, got %d", expectedBlobNumber, blobResponse.BlobNumber)
				return
			}

			if blobResponse.Error != nil {
				errorChannel <- blobResponse.Error
				return
			}

			expectedBlobNumber++

			if len(blobResponse.Contents) == 0 {
				break
			}

			if _, err := outputWriter.Write(blobResponse.Contents); err != nil {
				errorChannel <- fmt.Errorf("error writing to output: %w", err)
				return
			}

			pool.Put(blobResponse.Contents)

			hashChain := sha256.Sum256([]byte(encodedHashChain + blobResponse.EncodedMD5Hash))
			encodedHashChain = base64.StdEncoding.EncodeToString(hashChain[:])

			if blobResponse.EncodedMD5ChainHash != encodedHashChain {
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
		wg.Wait()
		return nil
	case err := <-errorChannel:
		cancel()
		wg.Wait()
		return err
	}
}

func readBufferStart(ctx context.Context, containerClient *ContainerClient) error {
	wait := atomic.Bool{}
	wait.Store(true)

	data, _, err := DownloadBlob(ctx, nil, containerClient, StartMetadataBlobName, &wait, nil, nil)
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

func pollForBufferEnd(ctx context.Context, containerClient *ContainerClient) error {
	wait := atomic.Bool{}
	wait.Store(false)

	for ctx.Err() == nil {
		data, _, err := DownloadBlob(ctx, nil, containerClient, EndMetadataBlobName, &wait, nil, nil)
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
			return ErrBufferFailedState
		default:
			log.Warn().Msgf("Buffer end blob has unexpected status '%s'", bufferEndMetadata.Status)
			return nil
		}
	}

	return nil
}

func DownloadBlob(ctx context.Context, metrics *TransferMetrics, containerClient *ContainerClient, blobPath string, waitForBlob *atomic.Bool, blobNumber *int64, finalBlobNumber *atomic.Int64) (*readData, *time.Time, error) {
	// The last error that occurred relating to reading the body. retryablehttp does not retry when these happen
	// because reading the body happens after the call to HttpClient.Do()
	var lastBodyReadError *responseBodyReadError

	for retryCount := 0; ; retryCount++ {
		start := time.Now()

		// Read this value before issuing the request. It will be set to false when the buffer end blob is written, which could happen
		// after the request is issued but before the response is read. By taking a snapshot now, we can avoid
		// that situation and we'll end up doing an extra retry.
		waitForBlobSnapshot := waitForBlob.Load()

		if blobNumber != nil {
			if num := finalBlobNumber.Load(); num >= 0 && num < *blobNumber {
				log.Ctx(ctx).Trace().Msg("Abandoning download after final blob")
				return nil, nil, errPastEndOfBlob
			}
		}

		req := containerClient.NewRequestWithRelativeUrl(ctx, http.MethodGet, blobPath, nil)
		AddCommonBlobRequestHeaders(req.Header)

		resp, err := containerClient.Do(req)
		if err != nil {
			return nil, nil, client.RedactHttpError(err)
		}

		respData, err := handleReadResponse(ctx, metrics, resp)
		if err == nil {
			log.Ctx(ctx).Trace().
				Int("contentLength", int(resp.ContentLength)).
				Dur("duration", time.Since(start)).
				Msg("Downloaded blob")

			if blobNumber != nil && len(respData.Data) == 0 {
				finalBlobNumber.Store(*blobNumber)
			}

			return respData, &start, nil
		}
		if err == errMd5Mismatch {
			if retryCount < 5 {
				log.Ctx(ctx).Debug().Msg("MD5 mismatch, retrying")
				continue
			} else {
				return nil, nil, fmt.Errorf("failed to read blob: %w", client.RedactHttpError(err))
			}
		}
		if err == ErrNotFound {
			if waitForBlobSnapshot {
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

			if blobNumber != nil {
				// If we get here then the the .bufferend blob has been read and we attempted to read a blob that doesn't exist.
				// This blob has either been deleted or is past the last final blob of size 0.
				if finalNum := finalBlobNumber.Load(); finalNum >= 0 {
					if finalNum < *blobNumber {
						// The blob we were attempting to download is after the final blob
						log.Ctx(ctx).Trace().Msg("Abandoning download after final blob")
						return nil, nil, errPastEndOfBlob
					}
					return nil, nil, fmt.Errorf("blob number %d was expected to exist but does not", *blobNumber)
				}

				// We don't yet know what the final blob number is, we we will just report back that the blob does not exist.
				// This will only become an error if this blob is before the final blob.
			}
			return nil, nil, err
		}

		if err == errServerBusy || err == errOperationTimeout {
			// These errors indicate that we have hit the limit of what the Azure Storage service can handle.
			// Note that the retryablehttp client will already have retried the request a number of times.
			if retryCount < 100 {
				continue
			}
		}

		if errors.Is(err, ctx.Err()) {
			// the context has been canceled
			return nil, nil, err
		}
		if err, ok := err.(*responseBodyReadError); ok {
			if lastBodyReadError == nil {
				lastBodyReadError = err
				retryCount = 0
			}

			if retryCount < MaxRetries {
				log.Ctx(ctx).Debug().Err(err).Msg("Error reading response body, retrying")

				// wait in the same way as retryablehttp
				wait := containerClient.innerClient.Backoff(containerClient.innerClient.RetryWaitMin, containerClient.innerClient.RetryWaitMax, retryCount, resp)
				timer := time.NewTimer(wait)
				select {
				case <-req.Context().Done():
					timer.Stop()
					containerClient.innerClient.HTTPClient.CloseIdleConnections()
					return nil, nil, req.Context().Err()
				case <-timer.C:
				}

				continue
			}
		}

		return nil, nil, client.RedactHttpError(err)
	}
}

type readData struct {
	Data   []byte
	Header http.Header
}

func handleReadResponse(ctx context.Context, metrics *TransferMetrics, resp *http.Response) (*readData, error) {
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		if resp.ContentLength < 0 {
			io.Copy(io.Discard, resp.Body)
			return nil, &responseBodyReadError{reason: errors.New("expected Content-Length header missing")}
		}

		buf := pool.Get(int(resp.ContentLength))
		bodyReader := resp.Body
		if metrics != nil {
			metrics.EnsureStarted(nil)
			bodyReader = &DownloadProgressReader{
				Reader:          resp.Body,
				TransferMetrics: metrics,
			}
		}
		_, err := io.ReadFull(bodyReader, buf)
		if err != nil {
			// return the buffer to the pool
			pool.Put(buf)
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
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
		switch resp.Header.Get(ErrorCodeHeader) {
		case "BlobNotFound":
			io.Copy(io.Discard, resp.Body)
			return nil, ErrNotFound
		case "ContainerNotFound":
			io.Copy(io.Discard, resp.Body)
			return nil, errBufferDoesNotExist
		}
	case http.StatusForbidden:
		switch resp.Header.Get(ErrorCodeHeader) {
		case "AuthenticationFailed":
			bodyBytes, _ := io.ReadAll(resp.Body)
			return nil, &InvalidAccessUrlError{
				Reason: extractAuthenticationErrorDetail(bodyBytes),
			}
		}
	case http.StatusInternalServerError:
		io.Copy(io.Discard, resp.Body)
		return nil, errOperationTimeout
	case http.StatusServiceUnavailable:
		io.Copy(io.Discard, resp.Body)
		return nil, errServerBusy
	}

	bodyBytes, _ := io.ReadAll(resp.Body)
	return nil, fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(bodyBytes))
}

type responseBodyReadError struct {
	reason error
}

func (e *responseBodyReadError) Error() string {
	return fmt.Sprintf("error reading response body: %v", e.reason)
}

func (e *responseBodyReadError) Unwrap() error {
	return e.reason
}

// extractAuthenticationErrorDetail extracts the AuthenticationErrorDetail from Azure Storage XML error response
func extractAuthenticationErrorDetail(xmlResponse []byte) string {
	var storageErrorBody struct {
		XMLName                   xml.Name `xml:"Error"`
		Code                      string   `xml:"Code"`
		Message                   string   `xml:"Message"`
		AuthenticationErrorDetail string   `xml:"AuthenticationErrorDetail"`
	}

	err := xml.Unmarshal(xmlResponse, &storageErrorBody)
	if err != nil {
		// If XML parsing fails, return empty string
		return ""
	}

	return strings.TrimSpace(storageErrorBody.AuthenticationErrorDetail)
}
