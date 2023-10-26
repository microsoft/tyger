package dataplane

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/hashicorp/go-retryablehttp"
	pool "github.com/libp2p/go-buffer-pool"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

const (
	DefaultWriteDop                = 16
	DefaultBlockSize               = 4 * 1024 * 1024
	EncodedMD5HashChainInitalValue = "MDAwMDAwMDAwMDAwMDAwMA=="
)

var (
	errBlobOverwrite = fmt.Errorf("unauthorized blob overwrite")
)

type writeOptions struct {
	dop        int
	blockSize  int
	httpClient *retryablehttp.Client
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

func WithWriteHttpClient(httpClient *retryablehttp.Client) WriteOption {
	return func(o *writeOptions) {
		o.httpClient = httpClient
	}
}

// If invalidHashChain is set to true, the value of the hash chain attached to the blob will
// always be the Inital Value. This should only be set for testing.
func Write(ctx context.Context, uri string, inputReader io.Reader, options ...WriteOption) error {
	writeOptions := &writeOptions{
		dop:       DefaultWriteDop,
		blockSize: DefaultBlockSize,
	}
	for _, o := range options {
		o(writeOptions)
	}

	ctx = log.With().Str("operation", "buffer write").Logger().WithContext(ctx)
	if writeOptions.httpClient == nil {
		var err error
		writeOptions.httpClient, err = CreateHttpClient("")
		if err != nil {
			return fmt.Errorf("failed to create http client: %w", err)
		}
	}

	httpClient := writeOptions.httpClient

	container, err := ValidateContainer(uri, httpClient)
	if err != nil {
		return fmt.Errorf("container validation failed: %w", err)
	}

	if err := writeStartMetadata(ctx, httpClient, container); err != nil {
		return err
	}

	outputChannel := make(chan BufferBlob, writeOptions.dop)
	errorChannel := make(chan error, 1)

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
				httpClient := NewClientWithLoggingContext(ctx, httpClient)
				var body any = bb.Contents
				if len(bb.Contents) == 0 {
					// This is a bit subtle, but if we send an empty or nil []byte body,
					// we will enpty with the Transfer-Encoding: chunked header, which
					// the blob service does not support.  So we send a nil body instead.
					body = nil
				}

				md5Hash := md5.Sum(bb.Contents)
				encodedMD5Hash := base64.StdEncoding.EncodeToString(md5Hash[:])

				previousMD5HashChain := <-bb.PreviousCumulativeHash

				md5HashChain := md5.Sum([]byte(previousMD5HashChain + encodedMD5Hash))
				encodedMD5HashChain := base64.StdEncoding.EncodeToString(md5HashChain[:])

				bb.CurrentCumulativeHash <- encodedMD5HashChain

				if err := uploadBlobWithRery(ctx, httpClient, blobUrl, body, encodedMD5Hash, encodedMD5HashChain); err != nil {
					log.Debug().Err(err).Msg("Encountered error uploading blob")
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

		previousHashChannel <- EncodedMD5HashChainInitalValue

		for {

			buffer := pool.Get(writeOptions.blockSize)
			bytesRead, err := io.ReadFull(inputReader, buffer)
			if blobNumber == 0 {
				metrics.Start()
			}

			if bytesRead > 0 {
				currentHashChannel := make(chan string, 1)

				outputChannel <- BufferBlob{
					BlobNumber:             blobNumber,
					Contents:               buffer[:bytesRead],
					PreviousCumulativeHash: previousHashChannel,
					CurrentCumulativeHash:  currentHashChannel,
				}

				previousHashChannel = currentHashChannel
				blobNumber++
			}

			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}

			if err != nil {
				errorChannel <- fmt.Errorf("error reading from input: %w", err)
			}
		}

		currentHashChannel := make(chan string, 1)

		outputChannel <- BufferBlob{
			BlobNumber:             blobNumber,
			Contents:               []byte{},
			PreviousCumulativeHash: previousHashChannel,
			CurrentCumulativeHash:  currentHashChannel,
		}
		close(outputChannel)

		wg.Wait()
		close(errorChannel)
	}()

	for err := range errorChannel {
		if ctx.Err() != nil {
			// this means the context was cancelled or timed out
			// use a new context to write the end metadata
			ctx, _ = context.WithTimeout(context.Background(), 3*time.Second)
		}
		writeEndMetadata(ctx, httpClient, container, BufferStatusFailed)
		return err
	}

	writeEndMetadata(ctx, httpClient, container, BufferStatusComplete)
	metrics.Stop()
	return nil
}

func writeStartMetadata(ctx context.Context, httpClient *retryablehttp.Client, container *Container) error {
	bufferStartMetadata := BufferStartMetadata{Version: CurrentBufferFormatVersion}
	startBytes, err := json.Marshal(bufferStartMetadata)
	if err != nil {
		panic(fmt.Errorf("failed to marshal start metadata: %w", err))
	}

	md5Hash := md5.Sum(startBytes)
	encodedMD5Hash := base64.StdEncoding.EncodeToString(md5Hash[:])

	return uploadBlobWithRery(ctx, httpClient, container.GetStartMetadataUri(), startBytes, encodedMD5Hash, "")
}

func writeEndMetadata(ctx context.Context, httpClient *retryablehttp.Client, container *Container, status string) {
	bufferEndMetadata := BufferEndMetadata{Status: status}
	endBytes, err := json.Marshal(bufferEndMetadata)
	if err != nil {
		panic(fmt.Errorf("failed to marshal end metadata: %w", err))
	}

	md5Hash := md5.Sum(endBytes)
	encodedMD5Hash := base64.StdEncoding.EncodeToString(md5Hash[:])

	err = uploadBlobWithRery(ctx, httpClient, container.GetEndMetadataUri(), endBytes, encodedMD5Hash, "")
	if err != nil {
		log.Warn().Err(err).Msg("Failed to upload optional metadata at the end of the transfer")
	}
}

func uploadBlobWithRery(ctx context.Context, httpClient *retryablehttp.Client, blobUrl string, body any, encodedMD5Hash string, encodedMD5HashChain string) error {
	start := time.Now()
	for i := 0; ; i++ {
		req, err := retryablehttp.NewRequest(http.MethodPut, blobUrl, body)
		if err != nil {
			return fmt.Errorf("unable to create request: %w", err)
		}

		req = req.WithContext(ctx)

		AddCommonBlobRequestHeaders(req.Header)
		req.Header.Add("x-ms-blob-type", "BlockBlob")

		req.Header.Add("Content-MD5", encodedMD5Hash)
		if encodedMD5HashChain != "" {
			req.Header.Add(HashChainHeader, encodedMD5HashChain)
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
				return fmt.Errorf("failed to upload blob: %w", RedactHttpError(err))
			}
		case errBlobOverwrite:
			if i == 0 {
				return fmt.Errorf("buffer cannot be overwritten: %w", RedactHttpError(err))
			}
			// When retrying failed writes, we might encounter the UnauthorizedBlobOverwrite if the original
			// write went through. In such cases, we should follow up with a HEAD request to verify the
			// Content-MD5 and x-ms-meta-cumulative_md5_chain match our expectations.
			req, err := retryablehttp.NewRequest(http.MethodHead, blobUrl, nil)
			if err != nil {
				return fmt.Errorf("unable to create HEAD request: %w", err)
			}

			resp, err := httpClient.Do(req)
			if err != nil {
				return fmt.Errorf("unable to send HEAD request: %w", err)
			}

			md5Header := resp.Header.Get("Content-MD5")
			md5ChainHeader := resp.Header.Get(HashChainHeader)

			if md5Header == encodedMD5Hash && md5ChainHeader == encodedMD5HashChain {
				log.Ctx(ctx).Debug().Msg("Failed blob write actually went through")
				return nil
			}
		case errBufferDoesNotExist:
			return err
		default:
			return fmt.Errorf("failed to upload blob: %w", RedactHttpError(err))
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
