package dataplane

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/hashicorp/go-retryablehttp"
	pool "github.com/libp2p/go-buffer-pool"
	"github.com/rs/zerolog/log"
)

const (
	DefaultWriteDop                = 16
	DefaultBlockSize               = 4 * 1024 * 1024
	EncodedMD5HashChainInitalValue = "MDAwMDAwMDAwMDAwMDAwMA=="
)

// If invalidHashChain is set to true, the value of the hash chain attached to the blob will
// always be the Inital Value. This should only be set for testing.
func Write(uri, proxyUri string, dop int, blockSize int, inputReader io.Reader, invalidHashChain bool) {
	ctx := log.With().Str("operation", "buffer write").Logger().WithContext(context.Background())

	httpClient, err := CreateHttpClient(proxyUri)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create http client")
	}
	container, err := ValidateContainer(uri, httpClient)
	if err != nil {
		log.Fatal().Err(err).Msg("Container validation failed")
	}

	outputChannel := make(chan BufferBlob, dop)
	errorChannel := make(chan struct{})

	hasFailed := func() bool {
		select {
		case <-errorChannel:
			return true
		default:
			return false
		}
	}

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

				var blobUrl string
				if bb.BlobName != "" {
					blobUrl = container.GetNamedBlobUri(bb.BlobName)
					ctx = log.Ctx(ctx).With().Str("blobName", bb.BlobName).Logger().WithContext(ctx)
				} else {
					blobUrl = container.GetBlobUri(bb.BlobNumber)
					ctx = log.Ctx(ctx).With().Int64("blobNumber", bb.BlobNumber).Logger().WithContext(ctx)
				}

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

				blobEncodedMD5HashChain := EncodedMD5HashChainInitalValue

				if bb.BlobName == "" {
					// Only created the Hash Chain on unnamed blobs
					previousMD5HashChain := <-bb.PreviousCumulativeHash

					md5HashChain := md5.Sum([]byte(previousMD5HashChain + encodedMD5Hash))
					encodedMD5HashChain := base64.StdEncoding.EncodeToString(md5HashChain[:])

					bb.CurrentCumulativeHash <- encodedMD5HashChain

					if !invalidHashChain {
						blobEncodedMD5HashChain = encodedMD5HashChain
					}
				} else {
					blobEncodedMD5HashChain = ""
				}

				for i := 0; ; i++ {
					req, err := retryablehttp.NewRequest(http.MethodPut, blobUrl, body)
					if err != nil {
						log.Err(err).Msg("Unable to create request")
						if !hasFailed() {
							close(errorChannel)
						}
					}

					AddCommonBlobRequestHeaders(req.Header)
					req.Header.Add("x-ms-blob-type", "BlockBlob")

					req.Header.Add("Content-MD5", encodedMD5Hash)
					if bb.BlobName == "" {
						req.Header.Add("x-ms-meta-cumulative_md5_chain", blobEncodedMD5HashChain)
					}

					resp, err := httpClient.Do(req)
					if err != nil {
						log.Err(RedactHttpError(err)).Msg("Unable to send request")
						if !hasFailed() {
							close(errorChannel)
						}
					}
					err = handleWriteResponse(resp)
					if err == nil {
						if !hasFailed() {
							log.Err(err).Msg("Write Failed")
							close(errorChannel)
						}
						break
					}

					if err == errMd5Mismatch {
						if i < 5 {
							log.Ctx(ctx).Debug().Msg("MD5 mismatch, retrying")
							continue
						}
					} else if err == errBlobOverwrite && i != 0 {
						// When retrying failed writes, we might encounter the UnauthorizedBlobOverwrite if the original
						// write went through. In such cases, we should follow up with a HEAD request to verify the
						// Content-MD5 and x-ms-meta-cumulative_md5_chain match our expectations.							req, err := retryablehttp.NewRequest(http.MethodHead, blobUrl, nil)
						req, err := retryablehttp.NewRequest(http.MethodHead, blobUrl, nil)
						if err != nil {
							log.Err(err).Msg("Unable to create HEAD request")
							if !hasFailed() {
								close(errorChannel)
							}
						}

						resp, err := httpClient.Do(req)
						if err != nil {
							log.Err(err).Msg("Unable to send HEAD request")
							if !hasFailed() {
								close(errorChannel)
							}
						}

						md5Header := resp.Header.Get("Content-MD5")
						md5ChainHeader := resp.Header.Get("x-ms-meta-cumulative_md5_chain")

						if md5Header == encodedMD5Hash && md5ChainHeader == blobEncodedMD5HashChain {
							log.Ctx(ctx).Debug().Msg("Failed blob write actually went through")
							break
						}

						log.Err(RedactHttpError(err)).Msg("Buffer cannot be overwritten")
						close(errorChannel)
					}

					if err != nil {
						log.Err(RedactHttpError(err)).Msg("Buffer cannot be overwritten")
						if !hasFailed() {
							close(errorChannel)
						}
					}
				}

				metrics.Update(uint64(len(bb.Contents)))

				log.Ctx(ctx).Trace().Int("contentLength", len(bb.Contents)).Dur("duration", time.Since(start)).Msg("Uploaded blob")

				pool.Put(bb.Contents)
			}
		}()
	}

	var blobNumber int64 = 0
	previousHashChannel := make(chan string, 1)

	previousHashChannel <- EncodedMD5HashChainInitalValue

	formatBlob := BufferFormat{Version: "0.1.0"}
	serializedBlob, _ := json.Marshal(formatBlob)

	outputChannel <- BufferBlob{
		BlobName:               ".bufferstart",
		BlobNumber:             -1,
		Contents:               serializedBlob,
		PreviousCumulativeHash: nil,
		CurrentCumulativeHash:  nil,
	}

	for {

		buffer := pool.Get(blockSize)
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

		if err == io.EOF || err == io.ErrUnexpectedEOF || hasFailed() {
			break
		}

		if err != nil {
			log.Fatal().Err(err).Msg("Error reading from input")
		}
	}

	currentHashChannel := make(chan string, 1)

	outputChannel <- BufferBlob{
		BlobNumber:             blobNumber,
		Contents:               []byte{},
		PreviousCumulativeHash: previousHashChannel,
		CurrentCumulativeHash:  currentHashChannel,
	}

	finalizationBlob := BufferFinalization{Status: "Completed"}

	if hasFailed() {
		finalizationBlob.Status = "Failed"
	}

	serializedBlob, _ = json.Marshal(finalizationBlob)

	outputChannel <- BufferBlob{
		BlobName:               ".bufferend",
		BlobNumber:             -1,
		Contents:               serializedBlob,
		PreviousCumulativeHash: nil,
		CurrentCumulativeHash:  nil,
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
