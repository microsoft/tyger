package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azqueue"
	"github.com/hashicorp/go-retryablehttp"
	"github.com/microsoft/tyger/cli/internal/client"
	"github.com/microsoft/tyger/cli/internal/dataplane"
	"github.com/microsoft/tyger/cli/internal/logging"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

const (
	sourceEndpoint      = "https://nihdatareconeastusbuf.blob.core.windows.net"
	destinationEndpoint = "https://nihwestus2buf.blob.core.windows.net"
	copyConcurrency     = 8
)

func main() {
	ctx := context.Background()
	zerolog.TimeFieldFormat = time.RFC3339Nano
	log.Logger = log.Logger.Level(zerolog.InfoLevel)
	zerolog.DefaultContextLogger = &log.Logger
	ctx = logging.SetLogSinkOnContext(ctx, os.Stderr)
	ctx = log.Logger.WithContext(ctx)

	if err := client.SetDefaultNetworkClientSettings(&client.ClientOptions{ProxyString: "none"}); err != nil {
		log.Ctx(ctx).Fatal().Err(err).Send()
	}

	cliCred, err := azidentity.NewAzureCLICredential(nil)
	if err != nil {
		log.Ctx(ctx).Fatal().Err(err).Msg("failed to get Azure CLI credential")
	}

	workloadCred, err := azidentity.NewWorkloadIdentityCredential(nil)
	if err != nil {
		log.Ctx(ctx).Fatal().Err(err).Msg("failed to get workload identity credential")
	}

	cred, err := azidentity.NewChainedTokenCredential([]azcore.TokenCredential{cliCred, workloadCred}, nil)
	if err != nil {
		log.Ctx(ctx).Fatal().Err(err).Msg("failed to create chained token credential")
	}

	dpClient, err := client.NewDataPlaneClient(nil)
	if err != nil {
		log.Ctx(ctx).Fatal().Err(err).Msg("failed to create data plane client")
	}

	dpClient.HTTPClient.Transport = &WithCredentialRoundtripper{
		inner: dpClient.HTTPClient.Transport,
		cred:  cred,
	}

	readOpts := []dataplane.ReadOption{dataplane.WithReadHttpClient(dpClient.Client), dataplane.WithRequireComplete(true)}
	writeOpts := []dataplane.WriteOption{dataplane.WithWriteHttpClient(dpClient.Client), dataplane.WithWriteDop(64)}

	queue, err := azqueue.NewQueueClient("https://jostairstygerbuf.queue.core.windows.net/buffers/", cred, nil)
	if err != nil {
		log.Ctx(ctx).Fatal().Err(err).Msg("failed to get queue client")
	}

	visibilityTimeout := int32((1 * time.Hour).Seconds())
	numberOfMessagesToDequeue := int32(10)

	wg := &sync.WaitGroup{}
	for i := 0; i < copyConcurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				messages, err := queue.DequeueMessages(ctx, &azqueue.DequeueMessagesOptions{VisibilityTimeout: &visibilityTimeout, NumberOfMessages: &numberOfMessagesToDequeue})
				if err != nil {
					log.Ctx(ctx).Fatal().Err(err).Msg("failed to dequeue message")
				}

				if len(messages.Messages) == 0 {
					return
				}

				for _, m := range messages.Messages {
					bufferId := *m.MessageText
					ctx = log.With().Str("container", bufferId).Logger().WithContext(ctx)
					err = copyBlob(ctx, bufferId, dpClient, readOpts, writeOpts)
					if err != nil {
						if errors.Is(err, dataplane.ErrBufferNotComplete) || errors.Is(err, dataplane.ErrBufferFailedState) {
							log.Warn().Err(err).Msg("skipping buffer")
						} else {
							log.Ctx(ctx).Fatal().Err(err).Msg("failed to copy blob")
						}
					}

					_, err = queue.DeleteMessage(ctx, *m.MessageID, *m.PopReceipt, nil)
					if err != nil {
						log.Ctx(ctx).Fatal().Err(err).Msg("failed to delete message")
					}
				}
			}
		}()
	}

	wg.Wait()
	log.Info().Msg("Done")
}

func copyBlob(ctx context.Context, bufferId string, dpClient *client.Client, readOpts []dataplane.ReadOption, writeOpts []dataplane.WriteOption) error {
	sourceContainerUrl, err := url.Parse(fmt.Sprintf("%s/%s", sourceEndpoint, bufferId))
	if err != nil {
		return fmt.Errorf("failed to parse URL: %w", err)
	}

	sourceContainer := dataplane.Container{URL: sourceContainerUrl}

	sourceBufferEndUrl := sourceContainer.GetEndMetadataUri()

	bufferEndReadRequest, err := retryablehttp.NewRequestWithContext(ctx, http.MethodGet, sourceBufferEndUrl, nil)
	if err != nil {
		log.Ctx(ctx).Fatal().Err(err).Msg("failed to create request")
	}
	dataplane.AddCommonBlobRequestHeaders(bufferEndReadRequest.Header)

	destinationContainerUrl, err := url.Parse(fmt.Sprintf("%s/%s", destinationEndpoint, bufferId))
	if err != nil {
		return fmt.Errorf("failed to parse URL: %w", err)
	}

	destinationContainer := dataplane.Container{URL: destinationContainerUrl}

	createContainerRequest, err := retryablehttp.NewRequestWithContext(ctx, http.MethodPut, fmt.Sprintf("%s?restype=container", destinationContainerUrl.String()), nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	dataplane.AddCommonBlobRequestHeaders(createContainerRequest.Header)
	createContainerResponse, err := dpClient.Do(createContainerRequest)
	if err != nil {
		return fmt.Errorf("failed to create container: %w", err)
	}
	io.Copy(io.Discard, createContainerResponse.Body)
	createContainerResponse.Body.Close()

	if createContainerResponse.StatusCode != http.StatusCreated {
		if createContainerResponse.StatusCode == http.StatusConflict {
			if err := deleteBlob(ctx, destinationContainer.GetStartMetadataUri(), dpClient); err != nil {
				return err
			}
			if err := deleteBlob(ctx, destinationContainer.GetEndMetadataUri(), dpClient); err != nil {
				return err
			}
		} else {
			return fmt.Errorf("failed to create container: %s", createContainerResponse.Status)
		}
	}

	pipeReader, pipeWriter := io.Pipe()
	defer pipeReader.Close()

	go func() {
		err := dataplane.Read(ctx, sourceContainer.URL, pipeWriter, readOpts...)
		pipeWriter.CloseWithError(err)
	}()

	return dataplane.Write(ctx, destinationContainerUrl, pipeReader, writeOpts...)
}

func deleteBlob(ctx context.Context, uri string, dpClient *client.Client) error {
	deleteBufferStartRequest, err := retryablehttp.NewRequestWithContext(ctx, http.MethodDelete, uri, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	dataplane.AddCommonBlobRequestHeaders(deleteBufferStartRequest.Header)

	resp, err := dpClient.Do(deleteBufferStartRequest)
	if err != nil {
		return fmt.Errorf("failed to delete blob: %w", err)
	}

	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusAccepted, http.StatusNotFound:
		return nil
	default:
		return fmt.Errorf("failed to delete blob: %s", resp.Status)
	}
}

type WithCredentialRoundtripper struct {
	inner http.RoundTripper
	cred  azcore.TokenCredential
	token azcore.AccessToken
}

func (w *WithCredentialRoundtripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if w.token.ExpiresOn.Compare(time.Now().Add(5*time.Minute)) < 0 {
		tok, err := w.cred.GetToken(req.Context(), policy.TokenRequestOptions{Scopes: []string{"https://storage.azure.com/.default"}})
		if err != nil {
			return nil, err
		}
		w.token = tok
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", w.token.Token))
	req.Header.Del("date")
	return w.inner.RoundTrip(req)
}
