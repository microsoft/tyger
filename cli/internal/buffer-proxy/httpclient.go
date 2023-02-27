package bufferproxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"time"

	pool "github.com/libp2p/go-buffer-pool"
	"github.com/rs/zerolog/log"
)

const (
	MaxRetries    = 6
	RetryDelay    = 800 * time.Millisecond
	MaxRetryDelay = 30 * time.Second
)

const (
	MetadataBlobName = ".buffer"
)

type RequestError interface {
	error
	IsRetryable() bool
	LogFatal(ctx context.Context, msg string)
}

type HttpRequestError struct {
	StatusCode int
	Body       []byte
}

func (e *HttpRequestError) Error() string {
	return fmt.Sprintf("HTTP Request error. status code: %d, body: %s", e.StatusCode, string(e.Body))
}

func (e *HttpRequestError) IsRetryable() bool {
	switch e.StatusCode {
	case
		http.StatusRequestTimeout,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func (e *HttpRequestError) LogFatal(ctx context.Context, msg string) {
	log.Ctx(ctx).Fatal().Err(e).Msg(msg)
}

var (
	ErrNotFound = &HttpRequestError{StatusCode: http.StatusNotFound}
)

type NetworkRequestError struct {
	Err error
}

func (e *NetworkRequestError) Error() string {
	return e.Err.Error()
}

func (e *NetworkRequestError) IsRetryable() bool {
	return true
}

func (e *NetworkRequestError) LogFatal(ctx context.Context, msg string) {
	log.Ctx(ctx).Fatal().Err(e).Msg(msg)
}

func CreateHttpClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConnsPerHost = 1000
	t.ResponseHeaderTimeout = 20 * time.Second

	return &http.Client{
		Transport: t,
		Timeout:   100 * time.Second,
	}
}

type Container struct {
	*url.URL
}

func (c *Container) GetBlobUri(blobNumber int) string {
	return c.URL.JoinPath(strconv.Itoa(blobNumber)).String()
}

func (c *Container) GetContainerName() string {
	return path.Base(c.Path)
}

func ValidateContainer(sasUri string, httpClient *http.Client) (*Container, error) {
	parsedUri, err := url.Parse(sasUri)
	if err != nil {
		return nil, err
	}

	metadataUri := parsedUri.JoinPath(MetadataBlobName)

	req, err := http.NewRequest(http.MethodGet, metadataUri.String(), nil)
	if err != nil {
		return nil, err
	}

	AddCommonBlobRequestHeaders(req.Header)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		io.Copy(io.Discard, resp.Body)
		return &Container{parsedUri}, nil
	case http.StatusNotFound:
		switch resp.Header.Get("x-ms-error-code") {
		case "BlobNotFound":
			io.Copy(io.Discard, resp.Body)
			return &Container{parsedUri}, nil
		case "ContainerNotFound":
			io.Copy(io.Discard, resp.Body)
			return nil, fmt.Errorf("container not found")
		}
	}

	bytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	return nil, fmt.Errorf("HTTP status code: %d. Body: %s", resp.StatusCode, string(bytes))
}

func AddCommonBlobRequestHeaders(header http.Header) {
	header.Add("Date", time.Now().Format(time.RFC1123Z))
	header.Add("x-ms-version", "2021-08-06")
}

func InvokeRequest(ctx context.Context, req *http.Request, client *http.Client) ([]byte, RequestError) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, &NetworkRequestError{err}
	}

	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusCreated:
		return nil, nil
	case http.StatusOK:
		if resp.ContentLength < 0 {
			log.Ctx(ctx).Fatal().Msg("Expected Content-Length header missing")
		}

		// rent a buffer from the pool
		buf := pool.Get(int(resp.ContentLength))

		_, err = io.ReadFull(resp.Body, buf)
		if err != nil {
			// return the buffer to the pool
			pool.Put(buf)
			return nil, &NetworkRequestError{err}
		}

		return buf, nil
	case http.StatusNotFound:
		io.Copy(io.Discard, resp.Body)
		return nil, ErrNotFound
	default:
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, &HttpRequestError{
			StatusCode: resp.StatusCode,
			Body:       bodyBytes,
		}
	}
}

func InvokeRequestWithRetries(ctx context.Context, createRequest func() *http.Request, client *http.Client) ([]byte, RequestError) {
	for retryNumber := 0; ; retryNumber++ {
		if retryNumber > 0 {
			time.Sleep(calcDelay(retryNumber))
		}
		req := createRequest()
		buf, err := InvokeRequest(ctx, req, client)
		if err == nil {
			return buf, nil
		}

		if err == ErrNotFound {
			return nil, err
		}

		if !err.IsRetryable() {
			return nil, err
		}

		if retryNumber >= MaxRetries {
			return nil, err
		} else {
			log.Ctx(ctx).Debug().Err(err).Msg("Retrying after error")
		}
	}
}

func calcDelay(retryNumber int) time.Duration {
	delay := (1 << (retryNumber - 1)) * RetryDelay
	if delay > MaxRetryDelay {
		delay = MaxRetryDelay
	}

	return delay
}
