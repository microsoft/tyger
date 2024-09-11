package main

import (
	"database/sql"
	"fmt"
	"iter"
	"os"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azqueue"
	"github.com/dustin/go-humanize"
	_ "github.com/lib/pq"
	"github.com/microsoft/tyger/cli/internal/logging"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"oras.land/oras-go/pkg/context"
)

func main() {
	ctx := context.Background()
	zerolog.TimeFieldFormat = time.RFC3339Nano
	log.Logger = log.Logger.Level(zerolog.InfoLevel)
	zerolog.DefaultContextLogger = &log.Logger
	ctx = logging.SetLogSinkOnContext(ctx, os.Stderr)
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
		log.Fatal().Err(err).Msg("failed to get queue client")
	}

	_, err = queue.ClearMessages(ctx, nil)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to clear messages")
	}

	// Get the buffer IDs
	bufferIds, err := GetBufferIds(cred)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to get buffer IDs")
	}

	var ttl int32 = -1
	enqueueOptions := &azqueue.EnqueueMessageOptions{
		TimeToLive: &ttl,
	}

	batch := make([]string, 0, 100)
	var count int64
	for id, err := range bufferIds {
		if err != nil {
			log.Fatal().Err(err).Msg("failed to get buffer ID")
		}
		batch = append(batch, id)

		count++
		if count%int64(cap(batch)) == 0 {
			message := strings.Join(batch, ",")
			_, err := queue.EnqueueMessage(ctx, message, enqueueOptions)
			if err != nil {
				log.Fatal().Err(err).Msg("failed to enqueue message")
			}

			batch = batch[:0]

			if count%1000 == 0 {
				log.Info().Msgf("Enqueued %s", humanize.Comma(count))
			}
		}
	}

	log.Info().Msgf("Enqueued %s", humanize.Comma(count))
	log.Info().Msg("Done")
}

func GetBufferIds(cred azcore.TokenCredential) (iter.Seq2[string, error], error) {
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

	rows, err := db.Query("SELECT id from buffers")
	if err != nil {
		db.Close()
		return nil, err
	}

	return func(yield func(string, error) bool) {
		defer db.Close()
		defer rows.Close()

		for rows.Next() {
			var id string
			err := rows.Scan(&id)
			if !yield(id, err) {
				break
			}
		}

		if err := rows.Err(); err != nil {
			yield("", err)
		}

	}, nil

}
