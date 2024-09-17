package main

import (
	"context"
	"fmt"
	"iter"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/microsoft/tyger/cli/internal/dataplane"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"golang.org/x/sync/semaphore"
)

const (
	parallelExportBufferCount   = 512
	maxExportConcurrentRequests = 1024
)

func newExportCommand(dbFlags *databaseFlags) *cobra.Command {
	sourceStorageEndpoint := ""
	destinationStorageEndpoint := ""
	cmd := &cobra.Command{
		Use:                   "export",
		Short:                 "Exports the buffers from the current Tyger instance a storage account",
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			ctx := cmd.Context()
			cred, err := createCredential()
			if err != nil {
				log.Ctx(cmd.Context()).Fatal().Err(err).Msg("Failed to create credentials")
			}

			sourceBlobServiceClient, err := azblob.NewClient(sourceStorageEndpoint, cred, &blobClientOptions)
			if err != nil {
				log.Ctx(ctx).Fatal().Err(err).Msg("failed to create blob service client")
			}

			destBlobServiceClient, err := azblob.NewClient(destinationStorageEndpoint, cred, &blobClientOptions)
			if err != nil {
				log.Ctx(ctx).Fatal().Err(err).Msg("failed to create blob service client")
			}

			sema := semaphore.NewWeighted(maxExportConcurrentRequests)

			transferMetrics := &dataplane.TransferMetrics{
				Context: ctx,
			}

			overallWg := sync.WaitGroup{}

			bufferChannel := make(chan bufferIdAndTags, parallelExportBufferCount)
			for range parallelExportBufferCount {
				go func() {
					for bufferIdAndTags := range bufferChannel {
						if err := copyBuffer(ctx, bufferIdAndTags, sourceBlobServiceClient, destBlobServiceClient, transferMetrics, sema); err != nil {
							log.Fatal().Err(err).Msg("failed to copy buffer")
						}
						transferMetrics.Update(0, 1)
						overallWg.Done()
					}
				}()
			}

			bufferPages, err := GetBufferIdsAndTags(ctx, dbFlags, cred)
			if err != nil {
				log.Fatal().Err(err).Msg("failed to get buffer IDs")
			}

			count := 0
			for page, err := range bufferPages {
				if err != nil {
					log.Fatal().Err(err).Msg("failed to get buffer IDs")
				}

				if count == 0 {
					transferMetrics.Start()
				}
				count += len(page)

				for _, bufferIdAndTags := range page {
					overallWg.Add(1)
					bufferChannel <- bufferIdAndTags
				}

			}

			close(bufferChannel)
			overallWg.Wait()

			transferMetrics.Stop()
		},
	}

	cmd.Flags().StringVar(&sourceStorageEndpoint, "source-storage-endpoint", "", "The storage account to export buffers from")
	cmd.Flags().StringVar(&destinationStorageEndpoint, "destination-storage-endpoint", "", "The storage account to export buffers to")
	cmd.MarkFlagRequired("source-storage-endpoint")
	cmd.MarkFlagRequired("destination-storage-endpoint")

	return cmd
}

func copyBuffer(ctx context.Context,
	bufferIdAndTags bufferIdAndTags,
	sourceBlobServiceClient *azblob.Client,
	destBlobServiceClient *azblob.Client,
	transferMetrics *dataplane.TransferMetrics,
	sema *semaphore.Weighted,
) error {
	containerId := bufferIdAndTags.id
	sourceContainerClient := sourceBlobServiceClient.ServiceClient().NewContainerClient(containerId)
	destContainerClient := destBlobServiceClient.ServiceClient().NewContainerClient(containerId)

	_, err := destContainerClient.Create(ctx, nil)
	if err != nil {
		if bloberror.HasCode(err, bloberror.ContainerAlreadyExists) {
			props, err := destContainerClient.GetProperties(ctx, nil)
			if err != nil {
				log.Ctx(ctx).Fatal().Err(err).Msg("failed to get container properties")
			}

			if status, ok := props.Metadata[exportedBufferStatusKey]; ok && status != nil && *status == exportedStatus {
				return nil
			}

		} else {
			log.Ctx(ctx).Fatal().Err(err).Msg("failed to create container")
		}
	}

	blobPager := sourceBlobServiceClient.NewListBlobsFlatPager(containerId, nil)

	bufferWaitGoup := sync.WaitGroup{}

	for blobPager.More() {
		blobPage, err := blobPager.NextPage(ctx)
		if err != nil {
			if bloberror.HasCode(err, bloberror.ContainerNotFound) {
				log.Ctx(ctx).Warn().Msgf("container '%s' not found", containerId)
				break
			}
			log.Ctx(ctx).Fatal().Err(err).Msg("failed to get page of blobs")
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

	tags := make(map[string]*string, len(bufferIdAndTags.tags)+1)
	exportedStatus := exportedStatus
	tags[exportedBufferStatusKey] = &exportedStatus
	for k, v := range bufferIdAndTags.tags {
		tags[fmt.Sprintf("customTagPrefix%s", k)] = &v
	}

	_, err = destContainerClient.SetMetadata(ctx, &container.SetMetadataOptions{Metadata: tags})
	if err != nil {
	}

	return nil
}

func GetBufferIdsAndTags(ctx context.Context, dbFlags *databaseFlags, cred azcore.TokenCredential) (iter.Seq2[[]bufferIdAndTags, error], error) {
	pool, err := createDatabaseConnectionPool(ctx, dbFlags, cred)
	if err != nil {
		return nil, err
	}

	return func(yield func([]bufferIdAndTags, error) bool) {
		conn, err := pool.Acquire(ctx)
		if err != nil {
			yield(nil, fmt.Errorf("failed to acquire database connection: %w", err))
			return
		}

		defer pool.Close()
		defer conn.Release()

		lastCreatedAt := time.Time{}
		lastBufferId := ""
		const pageSize = 8192
		pageCount := 0

		for {
			rows, err := conn.Query(ctx,
				`WITH matches AS (
					SELECT created_at, id
					FROM buffers
					WHERE (created_at, id) > ($1, $2)
					ORDER BY created_at ASC, id ASC
					LIMIT $3
				)
				SELECT matches.created_at, matches.id, tag_keys.name, tags.value
				FROM matches
				LEFT JOIN tags ON
					matches.created_at = tags.created_at AND matches.id = tags.id
				LEFT JOIN tag_keys on tags.key = tag_keys.id
				ORDER BY matches.created_at ASC, matches.id ASC`,
				lastCreatedAt, lastBufferId, pageSize)
			if err != nil {
				yield(nil, fmt.Errorf("failed to query database: %w", err))
				return
			}

			var page []bufferIdAndTags
			if pageCount == 0 {
				page = make([]bufferIdAndTags, 0, 1024)
			} else {
				page = make([]bufferIdAndTags, 0, pageSize)
			}
			pageCount++

			current := bufferIdAndTags{}

			for rows.Next() {
				var id string
				var tagKey *string
				var tagValue *string
				err := rows.Scan(&lastCreatedAt, &id, &tagKey, &tagValue)
				if err != nil {
					if yield(nil, err) {
						return
					}
				}

				lastBufferId = id
				if id != current.id {
					if current.id != "" {
						page = append(page, current)
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
				page = append(page, current)
			}

			if err := rows.Err(); err != nil {
				rows.Close()
				yield(nil, err)
				return
			}

			rows.Close()

			if len(page) == 0 {
				return
			}

			if !yield(page, nil) {
				return
			}
		}

	}, nil

}
