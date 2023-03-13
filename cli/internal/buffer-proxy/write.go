package bufferproxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/hashicorp/go-retryablehttp"
	pool "github.com/libp2p/go-buffer-pool"
	"github.com/rs/zerolog/log"
)

const (
	DefaultWriteDop  = 16
	DefaultBlockSize = 4 * 1024 * 1024
)

func Write(uri string, dop int, blockSize int, inputFile *os.File) {
	ctx := log.With().Str("operation", "buffer write").Logger().WithContext(context.Background())
	httpClient := CreateHttpClient()
	container, err := ValidateContainer(uri, httpClient)
	if err != nil {
		log.Fatal().Err(err).Msg("Container validation failed")
	}

	outputChannel := make(chan BufferBlob, dop)

	wg := sync.WaitGroup{}
	wg.Add(dop)

	metrics := TransferMetrics{
		Context:   ctx,
		Container: container,
	}

	for i := 0; i < dop; i++ {
		go func() {
			defer wg.Done()
			for bb := range outputChannel {
				start := time.Now()

				blobUrl := container.GetBlobUri(bb.BlobNumber)
				ctx := log.Ctx(ctx).With().Int64("blobNumber", bb.BlobNumber).Logger().WithContext(ctx)

				var body any = bb.Contents
				if len(bb.Contents) == 0 {
					// This is a bit subtle, but if we send an empty or nil []byte body,
					// we will enpty with the Transfer-Encoding: chunked header, which
					// the blob service does not support.  So we send a nil body instead.
					body = nil
				}

				req, err := retryablehttp.NewRequest(http.MethodPut, blobUrl, body)
				if err != nil {
					log.Fatal().Err(err).Msg("Unable to create request")
				}

				AddCommonBlobRequestHeaders(req.Header)
				req.Header.Add("x-ms-blob-type", "BlockBlob")

				resp, err := NewClientWithLoggingContext(ctx, httpClient).Do(req)
				if err != nil {
					log.Fatal().Err(RedactHttpError(err)).Msg("Unable to send request")
				}
				err = handleWriteResponse(resp)
				if err != nil {
					log.Fatal().Err(RedactHttpError(err)).Msg("Unable to write blob")
				}

				metrics.Update(uint64(len(bb.Contents)))

				log.Ctx(ctx).Trace().Int("contentLength", len(bb.Contents)).Dur("duration", time.Since(start)).Msg("Uploaded blob")

				pool.Put(bb.Contents)
			}
		}()
	}

	var blobNumber int64 = 0
	for {

		buffer := pool.Get(blockSize)
		bytesRead, err := io.ReadFull(inputFile, buffer)
		if blobNumber == 0 {
			metrics.Start()
		}

		if bytesRead > 0 {
			outputChannel <- BufferBlob{
				BlobNumber: blobNumber,
				Contents:   buffer[:bytesRead],
			}

			blobNumber++
		}

		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}

		if err != nil {
			log.Fatal().Err(err).Msg("Error reading from input")
		}
	}

	outputChannel <- BufferBlob{
		BlobNumber: blobNumber,
		Contents:   []byte{},
	}
	close(outputChannel)

	wg.Wait()
	metrics.Stop()
}

func handleWriteResponse(resp *http.Response) error {
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusCreated:
		io.Copy(io.Discard, resp.Body)
		return nil
	default:
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(bodyBytes))
	}
}
