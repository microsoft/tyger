package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	bufferproxy "dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/buffer-proxy"
	"github.com/alecthomas/units"
	pool "github.com/libp2p/go-buffer-pool"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

func newWriteCommand() *cobra.Command {
	intputFilePath := ""
	dop := 16
	blockSizeString := "4MiB"

	cmd := &cobra.Command{
		Use:   "write BUFFER_ACCESS_STRING [--input FILE]",
		Short: "Writes to a buffer",
		Long: `Write data to a buffer using the given access string.

The access string can be a SAS URI to an Azure Storage container or the path to a file
containing a SAS URI.`,
		DisableFlagsInUseLine: true,
		Args:                  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var inputFile *os.File
			if intputFilePath != "" {
				var err error
				inputFile, err = os.Open(intputFilePath)
				if err != nil {
					log.Fatal().Err(err).Msg("Unable to open input file for reading")
				}
				defer inputFile.Close()
			} else {
				inputFile = os.Stdin
			}

			if dop < 1 {
				return errors.New("the degree of parallelism (dop) must be at least 1")
			}

			if blockSizeString != "" && blockSizeString[len(blockSizeString)-1] != 'B' {
				blockSizeString += "B"
			}
			parsedBlockSize, err := units.ParseBase2Bytes(blockSizeString)
			if err != nil {
				return err
			}
			blockSize := int(parsedBlockSize)

			uri, err := bufferproxy.GetUriFromAccessString(args[0])
			if err != nil {
				log.Fatal().Err(err).Msg("Invalid buffer access string")
			}

			httpClient := bufferproxy.CreateHttpClient()
			container, err := bufferproxy.ValidateContainer(uri, httpClient)
			if err != nil {
				log.Fatal().Err(err).Msg("Container validation failed")
			}

			outputChannel := make(chan BufferBlob, dop)
			ctx := context.Background()

			wg := sync.WaitGroup{}
			wg.Add(dop)

			metrics := bufferproxy.TransferMetrics{
				Container: container,
			}

			for i := 0; i < dop; i++ {
				go func() {
					defer wg.Done()
					for bb := range outputChannel {
						start := time.Now()

						blobUrl := container.GetBlobUri(bb.BlobNumber)
						ctx := log.With().Int("blobNumber", bb.BlobNumber).Logger().WithContext(ctx)

						_, err := bufferproxy.InvokeRequestWithRetries(ctx,
							func() *http.Request {
								req, err := http.NewRequest(http.MethodPut, blobUrl, bytes.NewReader(bb.Contents))
								if err != nil {
									log.Fatal().Err(err).Msg("Unable to create request")
								}

								bufferproxy.AddCommonBlobRequestHeaders(req.Header)
								req.Header.Add("x-ms-blob-type", "BlockBlob")
								return req
							},
							httpClient)

						if err != nil {
							err.LogFatal(ctx, "Failed to upload blob")
						}

						metrics.Update(uint64(len(bb.Contents)))

						log.Ctx(ctx).Trace().Int("contentLength", len(bb.Contents)).Dur("duration", time.Since(start)).Msg("Uploaded blob")
						// return the buffer to the pool
						pool.Put(bb.Contents)
					}
				}()
			}

			blobNumber := 0
			for {
				// rent a buffer from the pool
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
					log.Fatal().Err(err).Msg("Error reading from stdin")
				}
			}

			outputChannel <- BufferBlob{
				BlobNumber: blobNumber,
				Contents:   []byte{},
			}
			close(outputChannel)

			wg.Wait()
			metrics.Stop()

			return nil
		},
	}

	cmd.Flags().StringVarP(&intputFilePath, "input", "i", intputFilePath, "The file to read from. If not specified, data is read from standard in.")
	cmd.Flags().IntVarP(&dop, "dop", "p", dop, "The degree of parallelism")
	cmd.Flags().StringVarP(&blockSizeString, "block-size", "b", blockSizeString, "Split the stream into blocks of this size.")
	return cmd
}
