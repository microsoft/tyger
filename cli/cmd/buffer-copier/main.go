// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/microsoft/tyger/cli/internal/cmd"
)

const (
	exportedBufferStatusKey = "tyger_exported_buffer_status"
	exportedStatus          = "exported"
	customTagPrefix         = "tyger_custom_tag_"
)

var (
	exportedBufferStatusKeyHttpHeaderCasing = http.CanonicalHeaderKey(exportedBufferStatusKey)
)

var (
	// set during build
	version = ""

	blobClientOptions = azblob.ClientOptions{
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
)

func main() {
	dbFlags := databaseFlags{}

	rootCommand := cmd.NewCommonRootCommand(version)
	rootCommand.Use = "buffer-copier"
	rootCommand.Long = `Export and import buffers from one Tyger instance to another`

	// add flags for the root command based on the commonFlags struct
	rootCommand.PersistentFlags().StringVar(&dbFlags.dbName, "db-name", "postgres", "The name of the database to use for exporting or importing buffers")
	rootCommand.PersistentFlags().StringVar(&dbFlags.dbHost, "db-host", "", "The host of the database to use for exporting or importing buffers")
	rootCommand.PersistentFlags().IntVar(&dbFlags.dbPort, "db-port", 5432, "The port of the database to use for exporting or importing buffers")
	rootCommand.PersistentFlags().StringVar(&dbFlags.dbUser, "db-user", "", "The user of the database to use for exporting or importing buffers")

	rootCommand.MarkPersistentFlagRequired("destination-storage-endpoint")
	rootCommand.MarkPersistentFlagRequired("db-host")
	rootCommand.MarkPersistentFlagRequired("db-user")

	rootCommand.AddCommand(newExportCommand(&dbFlags))
	rootCommand.AddCommand(newImportCommand(&dbFlags))

	err := rootCommand.Execute()
	if err != nil {
		os.Exit(1)
	}
}

// Do a quick check to see if we can reach the storage account. Do not wait for the retries to complete.
func verifyStorageAccountConnectivity(ctx context.Context, client *azblob.Client) error {
	resChan := make(chan any)
	go func() {
		_, err := client.ServiceClient().GetAccountInfo(ctx, nil)
		resChan <- err
		close(resChan)
	}()

	select {
	case <-resChan:
		return nil
	case <-time.After(time.Minute):
		return fmt.Errorf("failed to connect to storage endpoint %s", client.ServiceClient().URL())
	}
}

type databaseFlags struct {
	dbName string
	dbHost string
	dbPort int
	dbUser string
}

type bufferIdAndTags struct {
	id   string
	tags map[string]string
}

func createCredential() (azcore.TokenCredential, error) {
	cred := make([]azcore.TokenCredential, 0)
	cliCred, err := azidentity.NewAzureCLICredential(nil)
	if err == nil {
		cred = append(cred, cliCred)
	}

	workloadCred, err := azidentity.NewWorkloadIdentityCredential(nil)
	if err == nil {
		cred = append(cred, workloadCred)
	}

	return azidentity.NewChainedTokenCredential(cred, nil)
}

func createDatabaseConnectionPool(ctx context.Context, commonFlags *databaseFlags, cred azcore.TokenCredential) (*pgxpool.Pool, error) {
	connectionString := fmt.Sprintf("host=%s port=%d dbname=%s user=%s sslmode=verify-full", commonFlags.dbHost, commonFlags.dbPort, commonFlags.dbName, commonFlags.dbUser)
	config, err := pgxpool.ParseConfig(connectionString)
	if err != nil {
		return nil, fmt.Errorf("failed to parse database connection config: %w", err)
	}

	config.BeforeConnect = func(ctx context.Context, cc *pgx.ConnConfig) error {
		tokenResponse, err := cred.GetToken(context.Background(), policy.TokenRequestOptions{
			Scopes: []string{"https://ossrdbms-aad.database.windows.net/.default"},
		})
		if err != nil {
			return fmt.Errorf("failed to get database token: %w", err)
		}
		cc.Config.Password = tokenResponse.Token
		return nil
	}

	pool, err := pgxpool.NewWithConfig(ctx, config)

	if err != nil {
		return nil, fmt.Errorf("failed to create database connection pool: %w", err)
	}

	return pool, nil
}

func getStorageAccountEndpointFromId(ctx context.Context, pool *pgxpool.Pool, storageAccountId int) (string, error) {
	var sourceStorageEndpoint string
	err := pool.QueryRow(ctx, `SELECT endpoint FROM storage_accounts WHERE id = $1`, storageAccountId).Scan(&sourceStorageEndpoint)
	return sourceStorageEndpoint, err
}

type RoundripTransporter struct {
	inner http.RoundTripper
}

func (t *RoundripTransporter) Do(req *http.Request) (*http.Response, error) {
	return t.inner.RoundTrip(req)
}

func getCurrentPrincipal(ctx context.Context, cred azcore.TokenCredential) string {
	const unknownPrincipal = "unknown"
	tokenResponse, err := cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{"https://storage.azure.com/.default"}})
	if err != nil {
		return unknownPrincipal
	}

	claims := jwt.MapClaims{}
	_, _, err = jwt.NewParser().ParseUnverified(tokenResponse.Token, claims)
	if err != nil {
		return unknownPrincipal
	}

	oid, _ := claims["oid"].(string)
	appId, _ := claims["appid"].(string)
	mi, _ := claims["xms_mirid"].(string)
	idtyp, _ := claims["idtyp"].(string)

	if mi != "" {
		miString := fmt.Sprintf("managed identity %s", mi)
		if appId != "" {
			miString += fmt.Sprintf(" (app ID %s)", appId)
		}
		return miString
	}

	switch idtyp {
	case "user":
		if name, _ := claims["unique_name"].(string); name != "" {
			return name
		}

		if name, _ := claims["upn"].(string); name != "" {
			return name
		}

		if oid != "" {
			return fmt.Sprintf("user %s", oid)
		}

	case "app":
		if appId != "" {
			return fmt.Sprintf("app ID %s", appId)
		}

		if oid != "" {
			return fmt.Sprintf("app %s", oid)
		}
	}

	return unknownPrincipal
}
