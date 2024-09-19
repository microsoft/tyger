package main

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"fmt"
	"iter"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/jackc/pgx/v5"
	"github.com/microsoft/tyger/cli/internal/dataplane"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"golang.org/x/sync/semaphore"
)

const (
	parallelExportBufferCount   = 512
	maxExportConcurrentRequests = 1024
	exportPageSize              = 8192
)

func newExportCommand(dbFlags *databaseFlags) *cobra.Command {
	sourceStorageEndpoint := ""
	destinationStorageEndpoint := ""
	bufferIdTransform := func(id string) string { return id }
	filter := make(map[string]string)
	cmd := &cobra.Command{
		Use:                   "export",
		Short:                 "Exports the buffers from the current Tyger instance a storage account",
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			ctx := cmd.Context()
			if runId := os.Getenv("TYGER_RUN_ID"); runId != "" {
				ctx = log.Ctx(ctx).With().Str("runId", runId).Logger().WithContext(ctx)
			}
			cred, err := createCredential()
			if err != nil {
				log.Ctx(cmd.Context()).Fatal().Err(err).Msg("Failed to create credentials")
			}

			if hashIds, err := cmd.Flags().GetBool("hash-ids"); err != nil {
				panic(err)
			} else if hashIds {
				bufferIdTransform = hashBufferId
			}

			sourceBlobServiceClient, err := azblob.NewClient(sourceStorageEndpoint, cred, &blobClientOptions)
			if err != nil {
				log.Ctx(ctx).Fatal().Err(err).Msg("failed to create blob service client")
			}

			destBlobServiceClient, err := azblob.NewClient(destinationStorageEndpoint, cred, &blobClientOptions)
			if err != nil {
				log.Ctx(ctx).Fatal().Err(err).Msg("failed to create blob service client")
			}

			if err := verifyStorageAccountConnectivity(ctx, sourceBlobServiceClient); err != nil {
				log.Ctx(ctx).Fatal().Err(err).Msg("failed to connect to source storage account")
			}
			if err := verifyStorageAccountConnectivity(ctx, destBlobServiceClient); err != nil {
				log.Ctx(ctx).Fatal().Err(err).Msg("failed to connect to source storage account")
			}

			sema := semaphore.NewWeighted(maxExportConcurrentRequests)

			transferMetrics := &dataplane.TransferMetrics{
				Context: ctx,
			}

			overallWg := sync.WaitGroup{}

			ctx, cancel := context.WithCancelCause(ctx)
			defer cancel(nil)

			bufferChannel := make(chan bufferIdAndTags, parallelExportBufferCount)
			for range parallelExportBufferCount {
				go func() {
					for bufferIdAndTags := range bufferChannel {
						if err := copyBuffer(ctx, bufferIdAndTags, sourceBlobServiceClient, destBlobServiceClient, transferMetrics, sema, bufferIdTransform); err != nil {
							cancel(err)
						} else {
							transferMetrics.Update(0, 1)
						}
						overallWg.Done()
					}
				}()
			}

			count := 0
			for page, err := range getBufferIdsAndTags(ctx, dbFlags, filter, cred) {
				if err != nil {
					cancel(fmt.Errorf("failed to get buffer IDs and tags: %w", err))
					break
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

			doneChan := make(chan any)
			go func() {
				overallWg.Wait()
				close(doneChan)
			}()

			select {
			case <-doneChan:
			case <-ctx.Done():
			}

			err = context.Cause(ctx)
			if err != nil {
				if bloberror.HasCode(err, bloberror.AuthorizationFailure, bloberror.AuthorizationPermissionMismatch, bloberror.InvalidAuthenticationInfo) {
					log.Ctx(ctx).Fatal().Err(err).Msgf("Failed to access storage account. Ensure %s has Storage Blob Data Contributor access on the storage account %s", getCurrentPrincipal(context.Background(), cred), destinationStorageEndpoint)
				} else {
					log.Ctx(ctx).Fatal().Err(err).Msg("Failed to export buffers")
				}
			}

			transferMetrics.Stop()
		},
	}

	cmd.Flags().StringVar(&sourceStorageEndpoint, "source-storage-endpoint", "", "The storage account to export buffers from")
	cmd.Flags().StringVar(&destinationStorageEndpoint, "destination-storage-endpoint", "", "The storage account to export buffers to")
	cmd.Flags().StringToStringVar(&filter, "filter", filter, "key-value tags to filter the buffers to export")
	cmd.Flags().Bool("hash-ids", false, "Hash the buffer IDs before exporting them")
	cmd.Flags().MarkHidden("hash-ids")
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
	bufferIdTransform func(string) string,
) error {
	sourceContainerId := bufferIdAndTags.id
	destinationContainerId := bufferIdTransform(sourceContainerId)
	sourceContainerClient := sourceBlobServiceClient.ServiceClient().NewContainerClient(sourceContainerId)
	destContainerClient := destBlobServiceClient.ServiceClient().NewContainerClient(destinationContainerId)

	_, err := destContainerClient.Create(ctx, nil)
	if err != nil {
		if bloberror.HasCode(err, bloberror.ContainerAlreadyExists) {
			props, err := destContainerClient.GetProperties(ctx, nil)
			if err != nil {
				return fmt.Errorf("failed to get container properties: %w", err)
			}

			// Note: casing is normalized because this is coming from an HTTP header
			if status, ok := props.Metadata[exportedBufferStatusKeyHttpHeaderCasing]; ok && status != nil && *status == exportedStatus {
				return nil
			}

		} else {
			return fmt.Errorf("failed to create container: %w", err)
		}
	}

	blobPager := sourceBlobServiceClient.NewListBlobsFlatPager(sourceContainerId, nil)

	bufferWaitGoup := sync.WaitGroup{}

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	for blobPager.More() {
		blobPage, err := blobPager.NextPage(ctx)
		if err != nil {
			if bloberror.HasCode(err, bloberror.ContainerNotFound) {
				log.Ctx(ctx).Warn().Msgf("container '%s' not found", sourceContainerId)
				break
			}
			return fmt.Errorf("failed to get page of blobs: %w", err)
		}

		for _, blob := range blobPage.Segment.BlobItems {
			sourceBlobClient := sourceContainerClient.NewBlockBlobClient(*blob.Name)
			destBlobClient := destContainerClient.NewBlockBlobClient(*blob.Name)
			if err := sema.Acquire(ctx, 1); err != nil {
				// context canceled
				return err
			}

			bufferWaitGoup.Add(1)
			go func() {
				defer bufferWaitGoup.Done()
				defer sema.Release(1)
				for {
					_, err := destBlobClient.UploadBlobFromURL(ctx, sourceBlobClient.URL(), nil)
					if err != nil {
						if bloberror.HasCode(err, bloberror.ServerBusy) {
							continue
						}

						cancel(err)
						return
					}
					break
				}

				transferMetrics.Update(uint64(*blob.Properties.ContentLength), 0)
			}()
		}
	}

	bufferWaitGoup.Wait()

	err = context.Cause(ctx)
	if err == nil {
		tags := make(map[string]*string, len(bufferIdAndTags.tags)+1)
		exportedStatus := exportedStatus
		tags[exportedBufferStatusKey] = &exportedStatus
		for k, v := range bufferIdAndTags.tags {
			tags[customTagPrefix+k] = &v
		}

		_, err = destContainerClient.SetMetadata(ctx, &container.SetMetadataOptions{Metadata: tags})
		if err != nil {
			return fmt.Errorf("failed to set metadata: %w", err)
		}
	}

	return err
}

func getBufferIdsAndTags(ctx context.Context, dbFlags *databaseFlags, filter map[string]string, cred azcore.TokenCredential) iter.Seq2[[]bufferIdAndTags, error] {
	return func(yield func([]bufferIdAndTags, error) bool) {
		pool, err := createDatabaseConnectionPool(ctx, dbFlags, cred)
		if err != nil {
			yield(nil, fmt.Errorf("failed to create database connection pool: %w", err))
			return
		}
		defer pool.Close()

		filterTagIds := make(map[string]int)
		if len(filter) > 0 {
			tagNames := make([]string, 0, len(filter))
			for k := range filter {
				tagNames = append(tagNames, k)
			}

			keyRows, _ := pool.Query(ctx, `SELECT name, id FROM tag_keys WHERE name = ANY ($1)`, tagNames)

			var name string
			var id int
			_, err = pgx.ForEachRow(keyRows, []any{&name, &id}, func() error {
				filterTagIds[name] = id
				return nil
			})
			if err != nil {
				yield(nil, fmt.Errorf("failed to fetch keys: %w", err))
				return
			}

			if len(filterTagIds) != len(filter) {
				return
			}
		}

		lastCreatedAt := time.Time{}
		lastBufferId := ""
		pageCount := 0

		// we fetch the results in pages because otherwise this reader could be open for many hours.
		query, params := func() (string, []any) {
			queryBuilder := strings.Builder{}
			params := []any{lastCreatedAt, lastBufferId, exportPageSize}
			paramOffset := len(params)
			for k, v := range filter {
				params = append(params, filterTagIds[k])
				params = append(params, v)
			}

			var matchTable string
			if len(filter) > 0 {
				matchTable = "tags"
			} else {
				matchTable = "buffers"
			}

			queryBuilder.WriteString(`
			WITH matches AS (
				SELECT t0.created_at, t0.id
				FROM `)
			queryBuilder.WriteString(matchTable)
			queryBuilder.WriteString(" AS t0\n")

			if len(filter) > 0 {
				for i := range len(filter) - 1 {
					aliasNumber := i + 1
					queryBuilder.WriteString(fmt.Sprintf("INNER JOIN tags AS t%d ON t0.created_at = t%d.created_at AND t0.id = t%d.id\n", aliasNumber, aliasNumber, aliasNumber))
				}

				queryBuilder.WriteString("WHERE\n")

				for i := range len(filter) {
					if i > 0 {
						queryBuilder.WriteString("AND\n")
					}
					queryBuilder.WriteString(fmt.Sprintf("t%d.key = $%d AND t%d.value = $%d\n", i, paramOffset+1, i, paramOffset+2))
					paramOffset += 2
				}
			}

			if len(filter) == 0 {
				queryBuilder.WriteString("WHERE\n")
			} else {
				queryBuilder.WriteString("AND\n")
			}

			queryBuilder.WriteString(`
				(t0.created_at, t0.id) > ($1, $2)
				ORDER BY t0.created_at ASC, t0.id ASC
				LIMIT $3
			)
			SELECT matches.created_at, matches.id, tag_keys.name, tags.value
				FROM matches
				LEFT JOIN tags ON
					matches.created_at = tags.created_at AND matches.id = tags.id
				LEFT JOIN tag_keys on tags.key = tag_keys.id
				ORDER BY matches.created_at ASC, matches.id ASC`)
			return queryBuilder.String(), params
		}()

		for {
			params[0] = lastCreatedAt
			params[1] = lastBufferId

			rows, _ := pool.Query(ctx, query, params...)

			var page []bufferIdAndTags
			if pageCount == 0 {
				page = make([]bufferIdAndTags, 0, 1024)
			} else {
				page = make([]bufferIdAndTags, 0, exportPageSize)
			}
			pageCount++

			current := bufferIdAndTags{}

			for rows.Next() {
				var id string
				var tagKey *string
				var tagValue *string
				err := rows.Scan(&lastCreatedAt, &id, &tagKey, &tagValue)
				if err != nil {
					yield(nil, fmt.Errorf("failed to read buffers from database: %w", err))
					rows.Close()
					return
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
				yield(nil, fmt.Errorf("failed to read buffers from database: %w", err))
				return
			}

			if len(page) == 0 {
				return
			}

			if !yield(page, nil) {
				return
			}

			if len(page) < exportPageSize {
				return
			}
		}
	}
}

func hashBufferId(id string) string {
	hash := sha256.Sum256([]byte(id))
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(hash[:]))
}
