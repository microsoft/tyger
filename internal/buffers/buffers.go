package buffers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"dev.azure.com/msresearch/compimag/_git/tyger/internal/config"
	"dev.azure.com/msresearch/compimag/_git/tyger/internal/model"
	"dev.azure.com/msresearch/compimag/_git/tyger/internal/uniqueid"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/rs/zerolog/log"
)

var ErrInvalidConnectionString = errors.New("invalid connection string")

type BufferManager interface {
	CreateBuffer(ctx context.Context) (id string, err error)
	GetBuffer(ctx context.Context, id string) error
	GetSasUri(ctx context.Context, bufferId string, writeable bool, externalAccess bool) (string, error)
	HealthCheck(ctx context.Context) error
}

func NewBufferManager(config config.ConfigSpec) (BufferManager, error) {
	connectionStringProperties := make(map[string]string)
	for _, p := range strings.Split(config.StorageAccountConnectionString, ";") {
		if p == "" {
			continue
		}
		tokens := strings.SplitN(p, "=", 2)
		if len(tokens) != 2 {
			return nil, ErrInvalidConnectionString
		}

		connectionStringProperties[tokens[0]] = tokens[1]
	}

	cred, err := azblob.NewSharedKeyCredential(connectionStringProperties["AccountName"], connectionStringProperties["AccountKey"])
	if err != nil {
		return nil, ErrInvalidConnectionString
	}

	endpoint, ok := connectionStringProperties["BlobEndpoint"]
	if !ok {
		return nil, ErrInvalidConnectionString
	}

	serviceClient, err := azblob.NewServiceClientWithSharedKey(endpoint, cred, nil)
	if err != nil {
		return nil, err
	}

	_, err = serviceClient.GetProperties(context.Background())
	if err != nil {
		return nil, err
	}

	return &manager{serviceClient: serviceClient, storageEmulatorExternalHost: config.StorageEmulatorExternalHost}, nil
}

type manager struct {
	serviceClient               azblob.ServiceClient
	storageEmulatorExternalHost string
}

func (m manager) CreateBuffer(ctx context.Context) (id string, err error) {
	id = uniqueid.NewId()
	containerClient := m.serviceClient.NewContainerClient(id)

	_, err = containerClient.Create(ctx, nil)
	return id, err
}

func (m manager) GetBuffer(ctx context.Context, id string) error {
	containerClient := m.serviceClient.NewContainerClient(id)
	_, err := containerClient.GetProperties(ctx, nil)
	if err != nil {
		var storageError *azblob.StorageError
		if errors.As(err, &storageError) && storageError.StatusCode() == http.StatusNotFound {
			return model.ErrNotFound
		}
	}
	return err
}

func (m manager) GetSasUri(ctx context.Context, bufferId string, writeable bool, externalAccess bool) (string, error) {
	containerClient := m.serviceClient.NewContainerClient(bufferId)
	sasToken, err := containerClient.GetSASToken(
		azblob.BlobSASPermissions{Read: true, Add: writeable, Create: writeable, Write: writeable, Delete: writeable},
		time.Now(),
		time.Now().Add(time.Hour))

	if err != nil {
		var storageError *azblob.StorageError
		if errors.As(err, &storageError) && storageError.StatusCode() == http.StatusNotFound {
			return "", model.ErrNotFound
		}

		return "", err
	}

	if externalAccess && strings.HasPrefix(m.serviceClient.URL(), "http://") && len(m.storageEmulatorExternalHost) > 0 {
		return fmt.Sprintf("http://%s/%s?%s", m.storageEmulatorExternalHost, bufferId, sasToken.Encode()), nil
	}

	return fmt.Sprintf("%s?%s", containerClient.URL(), sasToken.Encode()), nil
}

func (m manager) HealthCheck(ctx context.Context) error {
	_, err := m.serviceClient.GetProperties(ctx)
	if err != nil {
		log.Ctx(ctx).Err(err).Send()
		return errors.New("error accessing storage")
	}

	return nil
}
