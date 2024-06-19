// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package dataplane

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
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
	DefaultTimeWindow			 = 0 // seconds
	EncodedHashChainInitialValue = "MDAwMDAwMDAwMDAwMDAwMA=="
)

var (
	errBlobOverwrite = fmt.Errorf("unauthorized blob overwrite")
)

type writeOptions struct {
	dop                     int
	blockSize               int
	timeWindow				int
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

func WithWriteTime(timeWindow int) WriteOption {
	return func(o *writeOptions) {
		o.timeWindow = timeWindow
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
func Write(ctx context.Context, uri *url.URL, inputReader io.Reader, options ...WriteOption) error {
	container := &Container{uri}
	writeOptions := &writeOptions{
		dop:                     DefaultWriteDop,
		blockSize:               DefaultBlockSize,
		timeWindow:				 DefaultTimeWindow,
		metadataEndWriteTimeout: 3 * time.Second,
	}
	for _, o := range options {
		o(writeOptions)
	}

	ctx = log.With().Str("operation", "buffer write").Logger().WithContext(ctx)
	if writeOptions.httpClient == nil {
		tygerClient, _ := controlplane.GetClientFromCache()
		if tygerClient != nil {
			writeOptions.httpClient = tygerClient.DataPlaneClient.Client
			writeOptions.connectionType = tygerClient.ConnectionType()
			if tygerClient.ConnectionType() == client.TygerConnectionTypeSsh && uri.Scheme == "http+unix" && !container.SupportsRelay() {
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

	metrics := TransferMetrics{
		Context:   ctx,
		Container: container,
	}

	for i := 0; i < writeOptions.dop; i++ {
		go func() {
			defer wg.Done()
			for bb := range outputChannel {
				blobUrl := container.GetBlobUri(bb.BlobNumber)
				ctx := log.Ctx(ctx).With().Int64("blobNumber", bb.BlobNumber).Logger().WithContext(ctx)
				var body any = bb.Contents
				if len(bb.Contents) == 0 {
					// This is a bit subtle, but if we send an empty or nil []byte body,
					// we will empty with the Transfer-Encoding: chunked header, which
					// the blob service does not support.  So we send a nil body instead.
					body = nil
				}

				md5Hash := md5.Sum(bb.Contents)
				encodedMD5Hash := base64.StdEncoding.EncodeToString(md5Hash[:])

				previousHashChain := <-bb.PreviousCumulativeHash

				hashChain := sha256.Sum256([]byte(previousHashChain + encodedMD5Hash))
				encodedHashChain := base64.StdEncoding.EncodeToString(hashChain[:])

				bb.CurrentCumulativeHash <- encodedHashChain

				if err := uploadBlobWithRetry(ctx, httpClient, blobUrl, body, encodedMD5Hash, encodedHashChain); err != nil {
					if !errors.Is(err, ctx.Err()) {
						log.Debug().Err(err).Msg("Encountered error uploading blob")
					}
					errorChannel <- err
					return
				}

				metrics.Update(uint64(len(bb.Contents)))

				pool.Put(bb.Contents)
			}
		}()
	}

	go func() {
		var blobNumber int64 = 0
		previousHashChannel := make(chan string, 1)

		previousHashChannel <- EncodedHashChainInitialValue

		failed := false
		for {
			var bytesRead int
			var buffer []byte
			var err error

			if(writeOptions.timeWindow != 0) {
				// wait time window, then consume from inputReader
				<-time.After(time.Second* time.Duration(writeOptions.timeWindow))
				buffer, err = io.ReadAll(inputReader)
				bytesRead = len(buffer)
				// ReadAll will never return EOF in this scenario,
				if(bytesRead == 0 && err == nil) {
					err = os.Stdin.SetDeadline(time.Now().Add(time.Minute))
				}
			} else {
				buffer = pool.Get(writeOptions.blockSize)
				bytesRead, err = io.ReadFull(inputReader, buffer)
			}

			fmt.Println("bytes read", bytesRead)
			fmt.Println("here is what err is", err)
			if blobNumber == 0 {
				metrics.Start()
			}

			if bytesRead > 0 {
				currentHashChannel := make(chan string, 1)

				blob := BufferBlob{
					BlobNumber:             blobNumber,
					Contents:               buffer[:bytesRead],
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
			}

			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}

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

func writeStartMetadata(ctx context.Context, httpClient *retryablehttp.Client, container *Container) error {
	bufferStartMetadata := BufferStartMetadata{Version: CurrentBufferFormatVersion}
	startMetadataUri := container.GetStartMetadataUri()

	// See if the start metadata blob already exists and error out if it does.
	req, err := retryablehttp.NewRequestWithContext(ctx, http.MethodHead, startMetadataUri, nil)
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

	return uploadBlobWithRetry(ctx, httpClient, startMetadataUri, startBytes, encodedMD5Hash, "")
}

func writeEndMetadata(ctx context.Context, httpClient *retryablehttp.Client, container *Container, status string) {
	bufferEndMetadata := BufferEndMetadata{Status: status}
	endBytes, err := json.Marshal(bufferEndMetadata)
	if err != nil {
		panic(fmt.Errorf("failed to marshal end metadata: %w", err))
	}

	md5Hash := md5.Sum(endBytes)
	encodedMD5Hash := base64.StdEncoding.EncodeToString(md5Hash[:])

	err = uploadBlobWithRetry(ctx, httpClient, container.GetEndMetadataUri(), endBytes, encodedMD5Hash, "")
	if err != nil {
		log.Warn().Err(err).Msg("Failed to upload optional metadata at the end of the transfer")
	}
}

func uploadBlobWithRetry(ctx context.Context, httpClient *retryablehttp.Client, blobUrl string, body any, encodedMD5Hash string, encodedHashChain string) error {
	start := time.Now()
	for i := 0; ; i++ {
		req, err := retryablehttp.NewRequestWithContext(ctx, http.MethodPut, blobUrl, body)
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
			break
		}

		switch err {
		case errMd5Mismatch:
			if i < 5 {
				log.Ctx(ctx).Debug().Msg("MD5 mismatch, retrying")
				continue
			} else {
				return fmt.Errorf("failed to upload blob: %w", client.RedactHttpError(err))
			}
		case errBlobOverwrite:
			// When retrying failed writes, we might encounter the UnauthorizedBlobOverwrite if the original
			// write went through. In such cases, we should follow up with a HEAD request to verify the
			// Content-MD5 and x-ms-meta-cumulative_hash_chain match our expectations.
			req, err := retryablehttp.NewRequest(http.MethodHead, blobUrl, nil)
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
		default:
			return fmt.Errorf("failed to upload blob: %w", client.RedactHttpError(err))
		}
	}

	if log.Ctx(ctx).GetLevel() >= zerolog.TraceLevel {
		parsedUrl, _ := url.Parse(blobUrl)
		e := log.Ctx(ctx).Trace().Str("blobPath", parsedUrl.Path).Dur("duration", time.Since(start))
		if bytesBody, ok := body.([]byte); ok {
			e = e.Int("contentLength", len(bytesBody))
		} else if body == nil {
			e = e.Int("contentLength", 0)
		}
		e.Msg("Uploaded blob")
	}

	return nil
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
		fallthrough
	case http.StatusBadRequest:
		if resp.Header.Get("x-ms-error-code") == "Md5Mismatch" {
			io.Copy(io.Discard, resp.Body)
			return errMd5Mismatch
		}
		fallthrough
	case http.StatusForbidden:
		if resp.Header.Get("x-ms-error-code") == "UnauthorizedBlobOverwrite" {
			io.Copy(io.Discard, resp.Body)
			return errBlobOverwrite
		}
		fallthrough
	default:
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(bodyBytes))
	}
}

type MergedContext struct {
	context.Context                 // The context that is is used for deadlines and cancellation
	valueSource     context.Context // The context used for values
}

func (c *MergedContext) Value(key any) any {
	return c.valueSource.Value(key)
}
