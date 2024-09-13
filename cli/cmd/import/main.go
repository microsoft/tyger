package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/service"
	"github.com/dustin/go-humanize"
	"github.com/jackc/pgx/v5"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
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

	tokenResponse, err := cred.GetToken(context.Background(), policy.TokenRequestOptions{
		Scopes: []string{"https://ossrdbms-aad.database.windows.net/.default"},
	})

	if err != nil {
		log.Ctx(ctx).Fatal().Err(err).Msg("failed to get token")
	}

	connStr := fmt.Sprintf("dbname=postgres host=jostairs-tyger-intelligent-bardeen.postgres.database.azure.com port=5432 sslmode=verify-full user=SC-kr331@microsoft.com password=%s", tokenResponse.Token)

	conConfig, err := pgx.ParseConfig(connStr)
	if err != nil {
		log.Ctx(ctx).Fatal().Err(err).Msg("failed to parse connection string")
	}

	conn, err := pgx.ConnectConfig(ctx, conConfig)
	if err != nil {
		log.Ctx(ctx).Fatal().Err(err).Msg("failed to connect to database")
	}
	_ = conn

	storageEndpoint := "https://jostairswestus2.blob.core.windows.net"
	client, err := azblob.NewClient(storageEndpoint, cred, nil)
	if err != nil {
		log.Ctx(ctx).Fatal().Err(err).Msg("failed to create blob client")
	}

	maxResults := int32(5000)
	batchSize := 25_000

	alphabet := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	channel := make(chan *service.ContainerItem, int(maxResults)*10)

	wg := sync.WaitGroup{}
	for _, r := range alphabet {
		wg.Add(1)
		go func() {
			defer wg.Done()
			prefix := string(r)
			pager := client.NewListContainersPager(&azblob.ListContainersOptions{Include: azblob.ListContainersInclude{Metadata: true}, MaxResults: &maxResults, Prefix: &prefix})
			for pager.More() {
				page, err := pager.NextPage(ctx)
				if err != nil {
					log.Ctx(ctx).Fatal().Err(err).Msg("failed to list containers")
				}

				for _, container := range page.ContainerItems {
					channel <- container
				}
			}
		}()
	}

	go func() {
		wg.Wait()
		close(channel)
	}()

	if err := bulkInsert(ctx, conn, batchSize, channel); err != nil {
		log.Ctx(ctx).Fatal().Err(err).Msg("failed to bulk insert")
	}
}

func bulkInsert(ctx context.Context, conn *pgx.Conn, batchSize int, containers <-chan *service.ContainerItem) error {
	totalCount := int64(0)
	page := make([]*service.ContainerItem, 0, batchSize)

	for container := range containers {
		totalCount++
		page = append(page, container)
		if len(page) == batchSize {
			if err := insertBatch(ctx, conn, page, totalCount); err != nil {
				return err
			}
			page = page[:0]
		}
	}

	if len(page) > 0 {
		if err := insertBatch(ctx, conn, page, totalCount); err != nil {
			return err
		}
	}
	return nil
}

func insertBatch(ctx context.Context, conn *pgx.Conn, page []*service.ContainerItem, totalCount int64) error {
	start := time.Now()
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

	bufferSource := pgx.CopyFromSlice(len(page), func(i int) ([]any, error) { return []any{page[i].Name, createdAt}, nil })
	if _, err := tx.CopyFrom(ctx, []string{"temp_buffers"}, []string{"id", "created_at"}, bufferSource); err != nil {
		return fmt.Errorf("failed to bulk copy data: %w", err)
	}

	// insert tags to temp table
	tagRows := make([][]any, 0, len(page))
	for _, container := range page {
		for k, v := range container.Metadata {
			if strings.HasPrefix(k, "tyger_custom_tag_") {
				tagRows = append(tagRows, []any{container.Name, k[len("tyger_custom_tag_"):], v})
			}
		}
	}

	tagSource := pgx.CopyFromRows(tagRows)
	if _, err := tx.CopyFrom(ctx, []string{"temp_tags"}, []string{"id", "key", "value"}, tagSource); err != nil {
		return fmt.Errorf("failed to bulk copy tags: %w", err)
	}

	r, err := tx.Exec(ctx, `
	INSERT INTO tag_keys (name)
	SELECT DISTINCT key
	FROM temp_tags
	ON CONFLICT (name) DO NOTHING;

	WITH ins_buffer AS MATERIALIZED (
		INSERT INTO buffers (id, created_at, etag)
		SELECT id, created_at, '0'
		FROM temp_buffers
		ON CONFLICT (id) DO NOTHING
		RETURNING id, created_at
	), mapped_tags AS (
		SELECT
			temp_tags.id AS id,
			ins_buffer.created_at AS created_at,
			tag_keys.id AS key,
			temp_tags.value as value
		FROM temp_tags
		INNER JOIN ins_buffer ON temp_tags.id = ins_buffer.id
		INNER JOIN tag_keys ON temp_tags.key = tag_keys.name
	)
	INSERT INTO tags (id, created_at, key, value)
	SELECT id, created_at, key, value
	FROM mapped_tags
	`)

	if err != nil {
		return fmt.Errorf("failed to insert buffers: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	log.Info().Str("duration", time.Since(start).String()).Int("count", int(r.RowsAffected())).Str("totalCount", humanize.Comma(totalCount)).Msg("inserted batch")

	return err
}

func isStdErrTerminal() bool {
	o, _ := os.Stderr.Stat()
	return (o.Mode() & os.ModeCharDevice) == os.ModeCharDevice
}
