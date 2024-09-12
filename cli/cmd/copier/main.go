package main

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azqueue"
	"github.com/microsoft/tyger/cli/internal/dataplane"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"golang.org/x/sync/semaphore"
)

const (
	sourceEndpoint      = "https://nihdatareconeastusbuf.blob.core.windows.net"
	destinationEndpoint = "https://jostairswestus2.blob.core.windows.net"
)

func main() {
	ctx := context.Background()
	zerolog.TimeFieldFormat = time.RFC3339Nano
	log.Logger = log.Logger.Level(zerolog.InfoLevel)
	var logSink io.Writer
	if isStdErrTerminal() {
		logSink = zerolog.ConsoleWriter{
			Out:        os.Stderr,
			TimeFormat: "2006-01-02T15:04:05.000Z07:00", // like RFC3339Nano, but always showing three digits for the fractional seconds
		}
	} else {
		logSink = os.Stderr
	}

	log.Logger = log.Output(logSink)
	zerolog.DefaultContextLogger = &log.Logger
	ctx = log.Logger.WithContext(ctx)

	creds := make([]azcore.TokenCredential, 0)
	cliCred, err := azidentity.NewAzureCLICredential(nil)
	if err == nil {
		creds = append(creds, cliCred)
	}

	workloadCred, err := azidentity.NewWorkloadIdentityCredential(nil)
	if err == nil {
		creds = append(creds, workloadCred)
	}

	cred, err := azidentity.NewChainedTokenCredential(creds, nil)
	if err != nil {
		log.Ctx(ctx).Fatal().Err(err).Msg("failed to create chained token credential")
	}

	queue, err := azqueue.NewQueueClient("https://jostairstygerbuf.queue.core.windows.net/buffers2/", cred, nil)
	if err != nil {
		log.Ctx(ctx).Fatal().Err(err).Msg("failed to get queue client")
	}

	visibilityTimeout := int32((1 * time.Hour).Seconds())
	messagesToDequeue := int32(20)

	clientOptions := azblob.ClientOptions{
		ClientOptions: azcore.ClientOptions{
			Transport: &RoundripTransporter{
				inner: &http.Transport{
					Proxy: http.ProxyFromEnvironment,
					DialContext: (&net.Dialer{
						Timeout:   30 * time.Second,
						KeepAlive: 30 * time.Second,
					}).DialContext,
					ForceAttemptHTTP2:     false,
					MaxIdleConns:          10000,
					MaxIdleConnsPerHost:   5000,
					IdleConnTimeout:       90 * time.Second,
					TLSHandshakeTimeout:   10 * time.Second,
					ExpectContinueTimeout: 1 * time.Second,
					TLSClientConfig: &tls.Config{
						MinVersion:    tls.VersionTLS12,
						Renegotiation: tls.RenegotiateFreelyAsClient,
					},
				}},
			Retry: policy.RetryOptions{
				MaxRetries: 50,
			},
		},
	}

	sourceBlobServiceClient, err := azblob.NewClient(sourceEndpoint, cred, &clientOptions)
	if err != nil {
		log.Ctx(ctx).Fatal().Err(err).Msg("failed to create blob service client")
	}

	destBlobServiceClient, err := azblob.NewClient(destinationEndpoint, cred, &clientOptions)
	if err != nil {
		log.Ctx(ctx).Fatal().Err(err).Msg("failed to create blob service client")
	}

	sema := semaphore.NewWeighted(2048)

	transferMetrics := &dataplane.TransferMetrics{
		Context: ctx,
	}

	transferMetrics.Start()

	for {
		r, err := queue.DequeueMessages(ctx, &azqueue.DequeueMessagesOptions{VisibilityTimeout: &visibilityTimeout, NumberOfMessages: &messagesToDequeue})
		if err != nil {
			log.Ctx(ctx).Fatal().Err(err).Msg("failed to dequeue message")
		}

		if len(r.Messages) == 0 {
			break
		}

		batchWaitGroup := sync.WaitGroup{}
		for _, m := range r.Messages {

			for _, containerId := range strings.Split(*m.MessageText, ",") {
				batchWaitGroup.Add(1)
				go func() {
					defer batchWaitGroup.Done()
					_, err = destBlobServiceClient.CreateContainer(ctx, containerId, nil)
					if err != nil {
						if !bloberror.HasCode(err, bloberror.ContainerAlreadyExists) {
							log.Ctx(ctx).Fatal().Err(err).Msg("failed to create container")
						}
					}

					sourceContainerClient := sourceBlobServiceClient.ServiceClient().NewContainerClient(containerId)
					destContainerClient := destBlobServiceClient.ServiceClient().NewContainerClient(containerId)
					blobPager := sourceBlobServiceClient.NewListBlobsFlatPager(containerId, nil)

					bufferWaitGoup := sync.WaitGroup{}
					bufferStart := time.Now()

					count := 0
					var bytes int64

					for blobPager.More() {
						blobPage, err := blobPager.NextPage(ctx)
						if err != nil {
							log.Ctx(ctx).Fatal().Err(err).Msg("failed to get next page")
						}

						for _, blob := range blobPage.Segment.BlobItems {
							sourceBlobClient := sourceContainerClient.NewBlockBlobClient(*blob.Name)
							destBlobClient := destContainerClient.NewBlockBlobClient(*blob.Name)
							bufferWaitGoup.Add(1)
							count++
							if blob.Properties.ContentLength != nil {
								bytes += *blob.Properties.ContentLength
							}

							sema.Acquire(ctx, 1)
							go func() {
								defer bufferWaitGoup.Done()
								defer sema.Release(1)
								for {
									_, err := destBlobClient.UploadBlobFromURL(ctx, sourceBlobClient.URL(), nil)
									if err != nil {
										if bloberror.HasCode(err, bloberror.ServerBusy) {
											log.Warn().Msg("throttled")
											time.Sleep(100 * time.Millisecond)
											continue
										}
										log.Fatal().Err(err).Msg("failed to copy blob")
									}
									break
								}

								transferMetrics.Update(uint64(*blob.Properties.ContentLength))
							}()
						}

					}

					bufferWaitGoup.Wait()
					log.Trace().Dur("duration", time.Since(bufferStart)).Int64("bytes", bytes).Str("bufferId", containerId).Msg("copied buffer")
				}()
			}

		}
		batchWaitGroup.Wait()
		for _, m := range r.Messages {
			_, err := queue.DeleteMessage(ctx, *m.MessageID, *m.PopReceipt, nil)
			if err != nil {
				log.Ctx(ctx).Fatal().Err(err).Msg("failed to delete message")
			}
			log.Trace().Msg("Deleted batch")
		}
	}

	transferMetrics.Stop()
}

type RoundripTransporter struct {
	inner http.RoundTripper
}

func (t *RoundripTransporter) Do(req *http.Request) (*http.Response, error) {
	return t.inner.RoundTrip(req)
}

func isStdErrTerminal() bool {
	o, _ := os.Stderr.Stat()
	return (o.Mode() & os.ModeCharDevice) == os.ModeCharDevice
}
