package main

import (
	"context"
	"crypto/tls"
	"database/sql"
	"fmt"
	"io"
	"iter"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	_ "github.com/lib/pq"
	"github.com/microsoft/tyger/cli/internal/dataplane"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"golang.org/x/sync/semaphore"
)

const (
	sourceEndpoint      = "https://nihdatareconeastusbuf.blob.core.windows.net"
	destinationEndpoint = "https://jostairswestus2.blob.core.windows.net"
	parallelBufferCount = 512
	tygerCopyingKey     = "tyger_copying"
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

	createContainerOptions := container.CreateOptions{
		Metadata: map[string]*string{tygerCopyingKey: nil},
	}

	sema := semaphore.NewWeighted(2048)

	transferMetrics := &dataplane.TransferMetrics{
		Context: ctx,
	}

	transferMetrics.Start()

	buffers, err := GetBufferIdsAndTags(cred)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to get buffer IDs")
	}
	overallWg := sync.WaitGroup{}

	bufferChannel := make(chan bufferIdAndTags, parallelBufferCount)
	for range parallelBufferCount {
		overallWg.Add(1)
		go func() {
			defer overallWg.Done()
			for bufferIdAndTags := range bufferChannel {
				containerId := bufferIdAndTags.id
				containerComplete := false
				_, err := destBlobServiceClient.CreateContainer(ctx, containerId, &createContainerOptions)
				if err != nil {
					if !bloberror.HasCode(err, bloberror.ContainerAlreadyExists) {
						log.Ctx(ctx).Fatal().Err(err).Msg("failed to create container")
					}
				}

				sourceContainerClient := sourceBlobServiceClient.ServiceClient().NewContainerClient(containerId)
				destContainerClient := destBlobServiceClient.ServiceClient().NewContainerClient(containerId)

				if !containerComplete {
					blobPager := sourceBlobServiceClient.NewListBlobsFlatPager(containerId, nil)

					bufferWaitGoup := sync.WaitGroup{}

					for blobPager.More() {
						blobPage, err := blobPager.NextPage(ctx)
						if err != nil {
							log.Ctx(ctx).Fatal().Err(err).Msg("failed to get next page")
						}

						for _, blob := range blobPage.Segment.BlobItems {
							sourceBlobClient := sourceContainerClient.NewBlockBlobClient(*blob.Name)
							destBlobClient := destContainerClient.NewBlockBlobClient(*blob.Name)
							bufferWaitGoup.Add(1)

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

								transferMetrics.Update(uint64(*blob.Properties.ContentLength), 0)
							}()
						}

					}

					bufferWaitGoup.Wait()
					transferMetrics.Update(0, 1)
				}

				var tags map[string]*string
				if len(bufferIdAndTags.tags) > 0 {
					tags = make(map[string]*string, len(bufferIdAndTags.tags))
					for k, v := range bufferIdAndTags.tags {
						tags[fmt.Sprintf("tyger_custom_tag_%s", k)] = &v
					}
				}

				_, err = destContainerClient.SetMetadata(ctx, &container.SetMetadataOptions{Metadata: tags})
				if err != nil {
					log.Ctx(ctx).Fatal().Err(err).Msg("failed to set metadata on container")
				}
			}
		}()

	}

	for bufferIdAndTags, err := range buffers {
		if err != nil {
			log.Fatal().Err(err).Msg("failed to get buffer IDs")
		}

		bufferChannel <- bufferIdAndTags
	}

	transferMetrics.Stop()
}

type bufferIdAndTags struct {
	id   string
	tags map[string]string
}

func GetBufferIdsAndTags(cred azcore.TokenCredential) (iter.Seq2[bufferIdAndTags, error], error) {
	tokenResponse, err := cred.GetToken(context.Background(), policy.TokenRequestOptions{
		Scopes: []string{"https://ossrdbms-aad.database.windows.net/.default"},
	})

	if err != nil {
		return nil, err
	}

	connStr := fmt.Sprintf("dbname=postgres host=nihdatarecon-tyger.postgres.database.azure.com port=5432 sslmode=verify-full user=SC-kr331@microsoft.com password=%s", tokenResponse.Token)
	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return nil, err
	}

	rows, err := db.Query(`
		SELECT buffers.id, tag_keys.name, tags.value
		FROM buffers
		LEFT JOIN tags ON
			buffers.created_at = tags.created_at AND buffers.id = tags.id
		LEFT JOIN tag_keys on tags.key = tag_keys.id
		ORDER BY buffers.created_at ASC, buffers.id ASC`)
	if err != nil {
		db.Close()
		return nil, err
	}

	return func(yield func(bufferIdAndTags, error) bool) {
		defer db.Close()
		defer rows.Close()

		current := bufferIdAndTags{}

		for rows.Next() {
			var id string
			var tagKey *string
			var tagValue *string
			err := rows.Scan(&id, &tagKey, &tagValue)
			if err != nil {
				if yield(bufferIdAndTags{}, err) {
					return
				}
			}

			if id != current.id {
				if current.id != "" {
					if !yield(current, nil) {
						return
					}
				}

				current = bufferIdAndTags{id: id}
				if tagKey != nil {
					current.tags = map[string]string{*tagKey: *tagValue}
				}
			}

			if tagKey != nil {
				current.tags[*tagKey] = *tagValue
			}
		}

		if current.id != "" {
			yield(current, nil)
		}

		if err := rows.Err(); err != nil {
			yield(bufferIdAndTags{}, err)
		}

	}, nil
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
