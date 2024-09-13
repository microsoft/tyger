package main

import (
	"database/sql"
	"fmt"
	"io"
	"iter"
	"os"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/dustin/go-humanize"
	_ "github.com/lib/pq"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"oras.land/oras-go/pkg/context"
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

	// queue, err := azqueue.NewQueueClient("https://jostairstygerbuf.queue.core.windows.net/buffers2/", cred, nil)
	// if err != nil {
	// 	log.Fatal().Err(err).Msg("failed to get queue client")
	// }

	// _, err = queue.ClearMessages(ctx, nil)
	// if err != nil {
	// 	log.Fatal().Err(err).Msg("failed to clear messages")
	// }

	// Get the buffer IDs
	bufferAndTags, err := GetBufferIdsAndTags(cred)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to get buffer IDs")
	}

	// var ttl int32 = -1
	// enqueueOptions := &azqueue.EnqueueMessageOptions{
	// 	TimeToLive: &ttl,
	// }

	var count int64
	for bufferIdAndTags, err := range bufferAndTags {
		if err != nil {
			log.Fatal().Err(err).Msg("failed to get buffer ID")
		}

		count++
		_ = bufferIdAndTags
		break
	}

	log.Info().Msgf("Enqueued %s", humanize.Comma(count))
	log.Info().Msg("Done")
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

func isStdErrTerminal() bool {
	o, _ := os.Stderr.Stat()
	return (o.Mode() & os.ModeCharDevice) == os.ModeCharDevice
}
