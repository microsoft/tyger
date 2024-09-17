package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/service"
	"github.com/dustin/go-humanize"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

const (
	listContainerPageSize = 5000 // max is 5000
	dbBatchSize           = 25_000
	containerPrefixChars  = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
)

func newImportCommand(dbFlags *databaseFlags) *cobra.Command {
	storageEndpoint := ""
	cmd := &cobra.Command{
		Use:                   "import",
		Short:                 "Imports buffers in a storage account to the current Tyger instance",
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			ctx := cmd.Context()
			cred, err := createCredential()
			if err != nil {
				log.Ctx(cmd.Context()).Fatal().Err(err).Msg("Failed to create credentials")
			}

			blobServiceClient, err := azblob.NewClient(storageEndpoint, cred, &blobClientOptions)
			if err != nil {
				log.Ctx(ctx).Fatal().Err(err).Msg("failed to create blob service client")
			}

			channel := make(chan *service.ContainerItem, listContainerPageSize*10)

			wg := sync.WaitGroup{}
			for _, r := range containerPrefixChars {
				wg.Add(1)
				go func() {
					defer wg.Done()
					prefix := string(r)
					pageSize := int32(listContainerPageSize)
					pager := blobServiceClient.NewListContainersPager(&azblob.ListContainersOptions{Include: azblob.ListContainersInclude{Metadata: true}, MaxResults: &pageSize, Prefix: &prefix})
					for pager.More() {
						page, err := pager.NextPage(ctx)
						if err != nil {
							log.Ctx(ctx).Fatal().Err(err).Msg("failed to list containers")
						}

						for _, container := range page.ContainerItems {
							for k, v := range container.Metadata {
								log.Info().Str("key", k).Str("value", *v).Msg("metadata")
							}
							if status, ok := container.Metadata[exportedBufferStatusKey]; ok && *status == exportedStatus {
								channel <- container
							}
						}
					}
				}()
			}

			go func() {
				wg.Wait()
				close(channel)
			}()

			pool, err := createDatabaseConnectionPool(ctx, dbFlags, cred)
			if err != nil {
				log.Ctx(ctx).Fatal().Err(err).Msg("failed to create database connection pool")
			}

			if err := bulkInsert(ctx, pool, dbBatchSize, channel); err != nil {
				log.Ctx(ctx).Fatal().Err(err).Msg("failed to bulk insert")
			}
		},
	}

	cmd.Flags().StringVar(&storageEndpoint, "storage-endpoint", "", "The storage account to import buffers from")
	cmd.MarkFlagRequired("storage-endpoint")

	return cmd
}

func bulkInsert(ctx context.Context, pool *pgxpool.Pool, batchSize int, containers <-chan *service.ContainerItem) error {
	totalCount := int64(0)
	page := make([]*service.ContainerItem, 0, batchSize)

	for container := range containers {
		totalCount++
		page = append(page, container)
		if len(page) == batchSize {
			if err := insertBatch(ctx, pool, page, totalCount); err != nil {
				return err
			}
			page = page[:0]
		}
	}

	if len(page) > 0 {
		if err := insertBatch(ctx, pool, page, totalCount); err != nil {
			return err
		}
	}
	return nil
}

func insertBatch(ctx context.Context, pool *pgxpool.Pool, containerBatch []*service.ContainerItem, totalCount int64) error {
	start := time.Now()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("failed to acquire connection: %w", err)
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	defer tx.Rollback(ctx)

	_, err = conn.Exec(ctx, `
	CREATE TEMPORARY TABLE temp_buffers (
		id TEXT,
		created_at timestamp with time zone
	)
	ON COMMIT DROP;

	CREATE TEMPORARY TABLE temp_tags (
		id TEXT,
		key TEXT,
		value TEXT

	)
	ON COMMIT DROP;
	`)

	if err != nil {
		return err
	}

	// insert buffers to temp table
	createdAt := time.Now().UTC()

	bufferSource := pgx.CopyFromSlice(len(containerBatch), func(i int) ([]any, error) { return []any{containerBatch[i].Name, createdAt}, nil })
	if _, err := tx.CopyFrom(ctx, []string{"temp_buffers"}, []string{"id", "created_at"}, bufferSource); err != nil {
		return fmt.Errorf("failed to bulk copy data: %w", err)
	}

	// insert tags to temp table
	tagRows := make([][]any, 0, len(containerBatch))
	for _, container := range containerBatch {
		for k, v := range container.Metadata {
			if strings.HasPrefix(k, customTagPrefix) {
				tagRows = append(tagRows, []any{container.Name, k[len(customTagPrefix):], v})
			}
		}
	}

	tagSource := pgx.CopyFromRows(tagRows)
	if _, err := tx.CopyFrom(ctx, []string{"temp_tags"}, []string{"id", "key", "value"}, tagSource); err != nil {
		return fmt.Errorf("failed to bulk copy tags: %w", err)
	}

	commandBatch := &pgx.Batch{}
	commandBatch.Queue(`
		INSERT INTO tag_keys (name)
		SELECT DISTINCT key
		FROM temp_tags
		WHERE NOT EXISTS (SELECT * FROM tag_keys WHERE name = temp_tags.key)
		ON CONFLICT (name) DO NOTHING
	`)

	newBufferCount := 0
	commandBatch.Queue(`
		WITH inserted_buffers AS (
			INSERT INTO buffers (id, created_at, etag)
			SELECT id, created_at, '0'
			FROM temp_buffers
			ON CONFLICT (id) DO NOTHING
			RETURNING id, created_at
		), mapped_tags AS (
			SELECT
				temp_tags.id AS id,
				inserted_buffers.created_at AS created_at,
				tag_keys.id AS key,
				temp_tags.value as value
			FROM temp_tags
			INNER JOIN inserted_buffers ON temp_tags.id = inserted_buffers.id
			INNER JOIN tag_keys ON temp_tags.key = tag_keys.name
		), inserted_tags AS (
			INSERT INTO tags (id, created_at, key, value)
			SELECT id, created_at, key, value
			FROM mapped_tags
		)
		SELECT COUNT(*) FROM inserted_buffers;
	`).QueryRow(func(row pgx.Row) error {
		return row.Scan(&newBufferCount)
	})

	batchResults := tx.SendBatch(ctx, commandBatch)
	err = batchResults.Close()
	if err != nil {
		return fmt.Errorf("failed to insert buffers: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	log.Info().Str("duration", time.Since(start).String()).Int("newBuffers", newBufferCount).Str("totalCount", humanize.Comma(totalCount)).Msg("inserted batch")

	return err
}
