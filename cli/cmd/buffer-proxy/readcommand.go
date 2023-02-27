package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"sync"
	"time"

	bufferproxy "dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/buffer-proxy"
	pool "github.com/libp2p/go-buffer-pool"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

func newReadCommand() *cobra.Command {
	outputFilePath := ""
	dop := 32
	cmd := &cobra.Command{
		Use:   "read BUFFER_ACCESS_STRING",
		Short: "Reads the contents of a buffer",
		Long: `Reads data from a buffer using the given access string.

The access string can be a SAS URI to an Azure Storage container or the path to a file
containing a SAS URI.`,
		DisableFlagsInUseLine: true,
		Args:                  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var outputFile *os.File
			if outputFilePath != "" {
				var err error
				outputFile, err = os.OpenFile(outputFilePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
				if err != nil {
					log.Fatal().Err(err).Msg("Unable to open output file for writing")
				}
				defer outputFile.Close()
			} else {
				outputFile = os.Stdout
			}

			if dop < 1 {
				return errors.New("the degree of parallelism (dop) must be at least 1")
			}

			uri, err := bufferproxy.GetUriFromAccessString(args[0])
			if err != nil {
				log.Fatal().Err(err).Msg("Invalid buffer access string")
			}

			httpClient := bufferproxy.CreateHttpClient()
			container, err := bufferproxy.ValidateContainer(uri, httpClient)
			if err != nil {
				log.Fatal().Err(err).Msg("Container validation failed")
			}

			ctx := context.Background()
			metrics := bufferproxy.TransferMetrics{
				Container: container,
			}
			metrics.Start()

			responseChannel := make(chan chan BufferBlob, dop*2)
			var lock sync.Mutex
			nextBlobNumber := 0

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
						ctx := log.With().Int("blobNumber", blobNumber).Logger().WithContext(ctx)
						bytes := WaitForBlobAndDownload(ctx, httpClient, blobUri)
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

				if _, err := outputFile.Write(blobResponse.Contents); err != nil {
					log.Fatal().Err(err).Msg("Error writing to output")
				}

				// return the buffer to the pool
				pool.Put(blobResponse.Contents)

				timeNow := time.Now()
				log.Trace().Int("blobNumber", blobResponse.BlobNumber).Dur("duration", timeNow.Sub(lastTime)).Msg("blob written to output")
				lastTime = timeNow
			}

			metrics.Stop()

			return nil
		},
	}

	cmd.Flags().StringVarP(&outputFilePath, "output", "o", outputFilePath, "The file write to. If not specified, data is written to standard out.")
	cmd.Flags().IntVarP(&dop, "dop", "p", dop, "The degree of parallelism")
	return cmd
}

func WaitForBlobAndDownload(ctx context.Context, httpClient *http.Client, blobUri string) []byte {
	for retryCount := 0; ; retryCount++ {
		start := time.Now()
		bytes, err := bufferproxy.InvokeRequestWithRetries(
			ctx,
			func() *http.Request {
				req, err := http.NewRequest(http.MethodGet, blobUri, nil)
				if err != nil {
					log.Ctx(ctx).Fatal().Err(err).Msg("Unable to create request")
				}

				bufferproxy.AddCommonBlobRequestHeaders(req.Header)
				return req
			},
			httpClient)

		if err == nil {
			log.Ctx(ctx).Trace().
				Int("contentLength", len(bytes)).
				Dur("duration", time.Since(start)).
				Msg("Downloaded blob")

			return bytes
		}

		if err == bufferproxy.ErrNotFound {
			log.Ctx(ctx).Trace().Msg("Waiting for blob")

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

		err.LogFatal(ctx, "Error downloading blob")
	}
}
