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
	"sync/atomic"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/dustin/go-humanize"
	_ "github.com/lib/pq"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

const (
	sourceEndpoint = "https://nihdatareconeastusbuf.blob.core.windows.net"
	dop            = 128
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

	// Get the buffer IDs
	bufferIds, err := GetBufferIds(cred)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to get buffer IDs")
	}

	totalCount := atomic.Int64{}
	totalBytes := atomic.Int64{}

	bufferIdChannel := make(chan string, 2048)
	wg := sync.WaitGroup{}

	go func() {
		last := totalCount.Load()
		for {
			start := time.Now()
			time.Sleep(2 * time.Second)
			end := time.Now()
			elapsed := end.Sub(start)
			newCount := totalCount.Load()
			buffersProcessed := newCount - last
			last = newCount
			log.Info().Msgf("Cumulative buffers processed: %s (current %s/s)", humanize.Comma(newCount), humanize.Comma(int64(float64(buffersProcessed)/elapsed.Seconds())))
		}
	}()

	for range dop {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range bufferIdChannel {
				bufferSize, err := getBufferSize(ctx, sourceBlobServiceClient, id)
				if err != nil {
					log.Fatal().Err(err).Msg("failed to get buffer size")
				}

				totalBytes.Add(bufferSize)
				totalCount.Add(int64(1))
			}
		}()
	}

	for id, err := range bufferIds {
		if err != nil {
			log.Fatal().Err(err).Msg("failed to get buffer ID")
		}
		bufferIdChannel <- id
	}

	close(bufferIdChannel)
	wg.Wait()

	log.Info().Msgf("Total buffers: %s", humanize.Comma(totalCount.Load()))
	log.Info().Msgf("Total data: %s", humanize.IBytes(uint64(totalBytes.Load())))
}

func getBufferSize(ctx context.Context, blobServiceClient *azblob.Client, bufferId string) (int64, error) {
	blobPager := blobServiceClient.NewListBlobsFlatPager(bufferId, nil)

	var totalBytes int64
	for blobPager.More() {
		blobPage, err := blobPager.NextPage(ctx)
		if err != nil {
			return 0, fmt.Errorf("failed to get next page: %w", err)
		}

		for _, blob := range blobPage.Segment.BlobItems {
			totalBytes += *blob.Properties.ContentLength
		}
	}

	return totalBytes, nil
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
