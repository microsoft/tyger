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
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"sync"
	"time"

	"github.com/hashicorp/go-retryablehttp"
	pool "github.com/libp2p/go-buffer-pool"
	"github.com/microsoft/tyger/cli/internal/client"
	"github.com/microsoft/tyger/cli/internal/controlplane"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

const (
	DefaultWriteDop              = 16
	DefaultBlockSize             = 4 * 1024 * 1024
	EncodedHashChainInitialValue = "MDAwMDAwMDAwMDAwMDAwMA=="
)

var (
	DefaultFlushInterval           = 1 * time.Second
	errBlobOverwrite               = fmt.Errorf("unauthorized blob overwrite")
	errBlobWritePermissionMismatch = fmt.Errorf("the access url does not allow write access")
)

type writeOptions struct {
	dop                     int
	blockSize               int
	flushInterval           time.Duration
	httpClient              *retryablehttp.Client
	metadataEndWriteTimeout time.Duration
	connectionType          client.TygerConnectionType
}

type WriteOption func(o *writeOptions)

func WithWriteDop(dop int) WriteOption {
	return func(o *writeOptions) {
		o.dop = dop
	}
}

func WithWriteBlockSize(blockSize int) WriteOption {
	return func(o *writeOptions) {
		o.blockSize = blockSize
	}
}

func WithWriteFlushInterval(flushInterval time.Duration) WriteOption {
	return func(o *writeOptions) {
		o.flushInterval = flushInterval
	}
}

func WithWriteHttpClient(httpClient *retryablehttp.Client) WriteOption {
	return func(o *writeOptions) {
		o.httpClient = httpClient
	}
}

func WithWriteMetadataEndWriteTimeout(timeout time.Duration) WriteOption {
	return func(o *writeOptions) {
		o.metadataEndWriteTimeout = timeout
	}
}

// If invalidHashChain is set to true, the value of the hash chain attached to the blob will
// always be the Inital Value. This should only be set for testing.
func Write(ctx context.Context, container *Container, inputReader io.Reader, options ...WriteOption) error {
	writeOptions := &writeOptions{
		dop:                     DefaultWriteDop,
		blockSize:               DefaultBlockSize,
		flushInterval:           DefaultFlushInterval,
		metadataEndWriteTimeout: 3 * time.Second,
	}
	for _, o := range options {
		o(writeOptions)
	}

	ctx = log.Ctx(ctx).With().
		Str("operation", "buffer write").
		Str("buffer", container.GetContainerName()).
		Logger().WithContext(ctx)

	if writeOptions.httpClient == nil {
		tygerClient, _ := controlplane.GetClientFromCache()
		if tygerClient != nil {
			writeOptions.httpClient = tygerClient.DataPlaneClient.Client
			writeOptions.connectionType = tygerClient.ConnectionType()
			if tygerClient.ConnectionType() == client.TygerConnectionTypeSsh && container.Scheme() == "http+unix" && !container.SupportsRelay() {
				httpClient, tunnelPool, err := createSshTunnelPoolClient(ctx, tygerClient, container, writeOptions.dop)
				if err != nil {
					return err
				}

				defer tunnelPool.Close()
				writeOptions.httpClient = httpClient
			}
		} else {
			writeOptions.httpClient = client.DefaultClient.Client
		}
	}

	httpClient := writeOptions.httpClient

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	if container.SupportsRelay() {
		return relayWrite(ctx, httpClient, writeOptions.connectionType, container, inputReader)
	}

	if err := writeStartMetadata(ctx, httpClient, container); err != nil {
		return err
	}

	outputChannel := make(chan BufferBlob, writeOptions.dop)
	errorChannel := make(chan error, writeOptions.dop+1)

	wg := sync.WaitGroup{}
	wg.Add(writeOptions.dop)

	metrics := NewTransferMetrics(ctx)

	for i := 0; i < writeOptions.dop; i++ {
		go func() {
			defer wg.Done()
			firstBlobForThisGoroutine := true
			for bb := range outputChannel {
				ctx := log.Ctx(ctx).With().Int64("blobNumber", bb.BlobNumber).Logger().WithContext(ctx)
				var body any
				if len(bb.Contents) == 0 {
					// This is a bit subtle, but if we send an empty or nil []byte body,
					// we will empty with the Transfer-Encoding: chunked header, which
					// the blob service does not support.  So we send a nil body instead.
					body = nil
				} else {
					body = retryablehttp.ReaderFunc(func() (io.Reader, error) {
						return &UploadProgressReader{
							Reader:          bytes.NewReader(bb.Contents),
							TransferMetrics: metrics,
						}, nil
					})
				}

				md5Hash := md5.Sum(bb.Contents)
				encodedMD5Hash := base64.StdEncoding.EncodeToString(md5Hash[:])

				previousHashChain := <-bb.PreviousCumulativeHash

				hashChain := sha256.Sum256([]byte(previousHashChain + encodedMD5Hash))
				encodedHashChain := base64.StdEncoding.EncodeToString(hashChain[:])

				bb.CurrentCumulativeHash <- encodedHashChain

				if firstBlobForThisGoroutine {
					metrics.EnsureStarted(nil)
					firstBlobForThisGoroutine = false
				}

				if err := uploadBlobWithRetry(ctx, httpClient, container, MakeBlobPath(bb.BlobNumber), body, encodedMD5Hash, encodedHashChain); err != nil {
					if !errors.Is(err, ctx.Err()) {
						log.Debug().Err(err).Msg("Encountered error uploading blob")
					}
					errorChannel <- err
					return
				}

				metrics.UpdateCompleted(uint64(len(bb.Contents)), 0)

				pool.Put(bb.Contents)
			}
		}()
	}

	go func() {
		var blobNumber int64 = 0
		previousHashChannel := make(chan string, 1)

		previousHashChannel <- EncodedHashChainInitialValue

		failed := false

		var blockSequence iter.Seq2[[]byte, error]
		if writeOptions.flushInterval > 0 {
			blockSequence = readInBlocksWithMaximumInterval(ctx, inputReader, writeOptions.blockSize, writeOptions.flushInterval)
		} else {
			blockSequence = readInBlocks(inputReader, writeOptions.blockSize)
		}

		for buffer, err := range blockSequence {
			currentHashChannel := make(chan string, 1)
			blob := BufferBlob{
				BlobNumber:             blobNumber,
				Contents:               buffer,
				PreviousCumulativeHash: previousHashChannel,
				CurrentCumulativeHash:  currentHashChannel,
			}
			select {
			case outputChannel <- blob:
			case <-ctx.Done():
				failed = true
			}

			if failed {
				break
			}

			previousHashChannel = currentHashChannel
			blobNumber++

			if err != nil {
				errorChannel <- fmt.Errorf("error reading from input: %w", err)
				failed = true
				break
			}
		}

		if !failed {
			currentHashChannel := make(chan string, 1)
			outputChannel <- BufferBlob{
				BlobNumber:             blobNumber,
				Contents:               []byte{},
				PreviousCumulativeHash: previousHashChannel,
				CurrentCumulativeHash:  currentHashChannel,
			}
		}

		close(outputChannel)

		wg.Wait()
		close(errorChannel)
	}()

	for err := range errorChannel {
		cancel()
		if ctx.Err() != nil {
			// this means the context was cancelled or timed out
			// use a new context to write the end metadata
			newCtx, cancel := context.WithTimeout(&MergedContext{Context: context.Background(), valueSource: ctx}, writeOptions.metadataEndWriteTimeout)
			defer cancel()
			ctx = newCtx
		}
		writeEndMetadata(ctx, httpClient, container, BufferStatusFailed)

		//lint:ignore SA4004 deliberately exiting after the first error
		return err
	}

	writeEndMetadata(ctx, httpClient, container, BufferStatusComplete)
	metrics.Stop()
	return nil
}

func readInBlocks(inputReader io.Reader, blockSize int) iter.Seq2[[]byte, error] {
	return func(yield func([]byte, error) bool) {
		for {
			buf := pool.Get(blockSize)
			n, err := io.ReadFull(inputReader, buf)
			if n > 0 {
				if !yield(buf[:n], nil) {
					return
				}
			}

			if err != nil {
				if err == io.EOF || err == io.ErrUnexpectedEOF {
					return
				}

				yield(nil, err)
				return
			}
		}
	}
}

func readInBlocksWithMaximumInterval(ctx context.Context, inputReader io.Reader, blockSize int, interval time.Duration) iter.Seq2[[]byte, error] {
	return func(yield func([]byte, error) bool) {
		dataCh := make(chan []byte)
		errCh := make(chan error, 1)
		mut := sync.Mutex{}

		// start a goroutine to read from the inputReader and write to the dataCh
		go func() {
			defer close(dataCh)
			buf := make([]byte, blockSize)
			bufPos := 0

			// start a timer to flush the buffer every interval by writing to dataCh
			var timer *time.Timer
			mut.Lock() // lock to ensure that `timer` is initialized before the callback uses it.
			timer = time.AfterFunc(interval, func() {
				mut.Lock()
				if bufPos == 0 {
					mut.Unlock()
				} else {
					// get a new slice from the pool and copy the contents of the buf into it.
					// reset bufPos to 0
					readSoFarBuf := pool.Get(bufPos)
					copy(readSoFarBuf, buf[:bufPos])
					bufPos = 0

					select {
					case dataCh <- readSoFarBuf:
						mut.Unlock()
					case <-ctx.Done():
						mut.Unlock()
						return
					}

					log.Trace().Msg("Flushed buffer")
				}

				timer.Reset(interval)
			})
			mut.Unlock()

			defer timer.Stop()

			for {
				var bufPosSnapshot int
				mut.Lock()
				bufPos = 0
				bufPosSnapshot = bufPos
				mut.Unlock()

				var err error
				for bufPosSnapshot < blockSize && err == nil {
					var n int
					n, err = inputReader.Read(buf[bufPosSnapshot:])
					if n > 0 {
						// check if the timer function fired and flushed the buffer while we were reading
						mut.Lock()
						if bufPos != bufPosSnapshot {
							if bufPos != 0 {
								panic("expected bufPos to be 0 after apparent flush")
							}
							// the buffer has been flushed, so we should not write to it
							copy(buf, buf[bufPosSnapshot:bufPosSnapshot+n])
						}
						bufPos += n
						bufPosSnapshot = bufPos
						mut.Unlock()
					}
				}

				// Yield if there is data in the buffer

				if bufPosSnapshot > 0 {
					mut.Lock()
					bufPosSnapshot = bufPos
					// set bufPos to 0 so that the timer function does not also try to flush
					bufPos = 0

					if bufPosSnapshot == 0 {
						mut.Unlock()
					} else {
						timer.Stop()
						block := pool.Get(blockSize)
						copy(block, buf[:bufPosSnapshot])

						select {
						case dataCh <- block[:bufPosSnapshot]:
							mut.Unlock()
						case <-ctx.Done():
							mut.Unlock()
							return
						}
					}
				}

				if err != nil {
					if err != io.EOF {
						errCh <- err
					}

					return
				}

				// Restart/reset the timer
				timer.Reset(interval)
			}
		}()

		// yield from the data and/or error channels

		for {
			select {
			case data, ok := <-dataCh:
				if !ok {
					return
				}
				if !yield(data, nil) {
					return
				}
			case err := <-errCh:
				yield(nil, err)
				return
			case <-ctx.Done():
				yield(nil, ctx.Err())
				return
			}
		}
	}
}

func writeStartMetadata(ctx context.Context, httpClient *retryablehttp.Client, container *Container) error {
	bufferStartMetadata := BufferStartMetadata{Version: CurrentBufferFormatVersion}

	// See if the start metadata blob already exists and error out if it does.
	containerUrl, err := container.GetValidAccessUrl(ctx)
	if err != nil {
		return fmt.Errorf("failed to get access URL: %w", err)
	}
	req, err := retryablehttp.NewRequestWithContext(ctx, http.MethodHead, containerUrl.JoinPath(StartMetadataBlobName).String(), nil)
	if err != nil {
		return fmt.Errorf("failed to create HEAD request: %w", err)
	}

	AddCommonBlobRequestHeaders(req.Header)

	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send HEAD request: %w", err)
	}

	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return fmt.Errorf("buffer cannot be overwritten")
	}

	startBytes, err := json.Marshal(bufferStartMetadata)
	if err != nil {
		panic(fmt.Errorf("failed to marshal start metadata: %w", err))
	}

	md5Hash := md5.Sum(startBytes)
	encodedMD5Hash := base64.StdEncoding.EncodeToString(md5Hash[:])

	return uploadBlobWithRetry(ctx, httpClient, container, StartMetadataBlobName, startBytes, encodedMD5Hash, "")
}

func writeEndMetadata(ctx context.Context, httpClient *retryablehttp.Client, container *Container, status BufferStatus) {
	bufferEndMetadata := BufferEndMetadata{Status: status}
	endBytes, err := json.Marshal(bufferEndMetadata)
	if err != nil {
		panic(fmt.Errorf("failed to marshal end metadata: %w", err))
	}

	md5Hash := md5.Sum(endBytes)
	encodedMD5Hash := base64.StdEncoding.EncodeToString(md5Hash[:])

	err = uploadBlobWithRetry(ctx, httpClient, container, EndMetadataBlobName, endBytes, encodedMD5Hash, "")
	if err != nil {
		log.Ctx(ctx).Warn().Err(err).Msg("Failed to upload optional metadata at the end of the transfer")
	}
}

func uploadBlobWithRetry(ctx context.Context, httpClient *retryablehttp.Client, container *Container, blobPath string, body any, encodedMD5Hash string, encodedHashChain string) error {
	start := time.Now()
	retriesDueToInvalidSas := 0
	for retryCount := 0; ; retryCount++ {
		containerUrl, err := container.GetValidAccessUrl(ctx)
		if err != nil {
			return fmt.Errorf("failed to get access URL: %w", err)
		}
		req, err := retryablehttp.NewRequestWithContext(ctx, http.MethodPut, containerUrl.JoinPath(blobPath).String(), body)
		if err != nil {
			return fmt.Errorf("unable to create request: %w", err)
		}

		AddCommonBlobRequestHeaders(req.Header)
		req.Header.Add("x-ms-blob-type", "BlockBlob")

		req.Header.Add(ContentMD5Header, encodedMD5Hash)
		if encodedHashChain != "" {
			req.Header.Add(HashChainHeader, encodedHashChain)
		}

		resp, err := httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("unable to send request: %w", err)
		}
		err = handleWriteResponse(resp)
		if err == nil {
			if log.Ctx(ctx).GetLevel() >= zerolog.TraceLevel {
				blobUrl := container.CurrentAccessUrl().JoinPath(blobPath)
				log.Ctx(ctx).Trace().Str("blobPath", blobUrl.Path).Dur("duration", time.Since(start)).Int64("contentLength", req.ContentLength).Msg("Uploaded blob")
			}
			return nil
		}

		switch err {
		case errMd5Mismatch:
			if retryCount < 5 {
				log.Ctx(ctx).Debug().Msg("MD5 mismatch, retrying")
				continue
			} else {
				return fmt.Errorf("failed to upload blob: %w", client.RedactHttpError(err))
			}
		case errBlobOverwrite:
			// When retrying failed writes, we might encounter the UnauthorizedBlobOverwrite if the original
			// write went through. In such cases, we should follow up with a HEAD request to verify the
			// Content-MD5 and x-ms-meta-cumulative_hash_chain match our expectations.
			containerUrl, err = container.GetValidAccessUrl(ctx)
			if err != nil {
				return fmt.Errorf("failed to get access URL: %w", err)
			}
			req, err := retryablehttp.NewRequest(http.MethodHead, containerUrl.JoinPath(blobPath).String(), nil)
			if err != nil {
				return fmt.Errorf("unable to create HEAD request: %w", err)
			}

			AddCommonBlobRequestHeaders(req.Header)

			resp, err := httpClient.Do(req)
			if err != nil {
				return fmt.Errorf("HEAD request failed: %w", err)
			}

			if resp.StatusCode == http.StatusOK {
				md5Header := resp.Header.Get(ContentMD5Header)
				hashChainHeader := resp.Header.Get(HashChainHeader)

				if md5Header == encodedMD5Hash && hashChainHeader == encodedHashChain {
					return nil
				}
			}

			return fmt.Errorf("buffer cannot be overwritten")
		case errBufferDoesNotExist:
			return err
		case ErrInvalidSas:
			if retriesDueToInvalidSas < 5 {
				retriesDueToInvalidSas++
				log.Ctx(ctx).Debug().Msg("SAS token expired, retrying")
				continue
			} else {
				return fmt.Errorf("failed to upload blob: %w", client.RedactHttpError(err))
			}
		case errServerBusy, errOperationTimeout:
			// These errors indicate that we have hit the limit of what the Azure Storage service can handle.
			// Note that the retryablehttp client will already have retried the request a number of times.
			if retryCount < 100 {
				continue
			}

			fallthrough
		default:
			return fmt.Errorf("failed to upload blob: %w", client.RedactHttpError(err))
		}
	}
}

func handleWriteResponse(resp *http.Response) error {
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusCreated:
		io.Copy(io.Discard, resp.Body)
		return nil
	case http.StatusNotFound:
		if resp.Header.Get("x-ms-error-code") == "ContainerNotFound" {
			return errBufferDoesNotExist
		}
	case http.StatusBadRequest:
		if resp.Header.Get("x-ms-error-code") == "Md5Mismatch" {
			io.Copy(io.Discard, resp.Body)
			return errMd5Mismatch
		}
	case http.StatusForbidden:
		switch resp.Header.Get("x-ms-error-code") {
		case "UnauthorizedBlobOverwrite":
			io.Copy(io.Discard, resp.Body)
			return errBlobOverwrite
		case "AuthorizationPermissionMismatch":
			io.Copy(io.Discard, resp.Body)
			return errBlobWritePermissionMismatch
		case "AuthenticationFailed":
			io.Copy(io.Discard, resp.Body)
			return ErrInvalidSas
		}
	case http.StatusInternalServerError:
		io.Copy(io.Discard, resp.Body)
		return errOperationTimeout
	case http.StatusServiceUnavailable:
		io.Copy(io.Discard, resp.Body)
		return errServerBusy
	}

	bodyBytes, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(bodyBytes))
}

type MergedContext struct {
	context.Context                 // The context that is is used for deadlines and cancellation
	valueSource     context.Context // The context used for values
}

func (c *MergedContext) Value(key any) any {
	return c.valueSource.Value(key)
}
