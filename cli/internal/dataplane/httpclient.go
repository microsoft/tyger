package dataplane

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/bits"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/rs/zerolog"
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

var (
	ErrNotFound      = errors.New("not found")
	errMd5Mismatch   = fmt.Errorf("MD5 mismatch")
	errBlobOverwrite = fmt.Errorf("unauthorized blob overwrite")
)

type responseBodyReadError struct {
	reason error
}

func (e *responseBodyReadError) Error() string {
	return fmt.Sprintf("error reading response body: %v", e.reason)
}

func CreateHttpClient(proxyUri string) (*retryablehttp.Client, error) {
	client := retryablehttp.NewClient()
	client.RetryMax = 6
	client.HTTPClient.Timeout = 100 * time.Second

	logger := &retryableClientLogger{
		Logger: &log.Logger,
	}
	client.Logger = logger
	client.ErrorHandler = func(resp *http.Response, err error, numTries int) (*http.Response, error) {
		return resp, err
	}
	client.CheckRetry = logger.CheckRetry

	transport := client.HTTPClient.Transport.(*http.Transport)
	transport.MaxIdleConnsPerHost = 1000
	transport.ResponseHeaderTimeout = 20 * time.Second

	if proxyUri != "" {
		proxyUrl, err := url.Parse(proxyUri)
		if err != nil {
			return nil, fmt.Errorf("invalid proxy url: %w", err)
		}
		transport.Proxy = http.ProxyURL(proxyUrl)
	}

	return client, nil
}

// Performs a shallow clone of the retryable client adjusting the logger
// to capture the context.
// The inner http.Client is reused.
func NewClientWithLoggingContext(ctx context.Context, client *retryablehttp.Client) *retryablehttp.Client {
	newClient := *client
	logger := &retryableClientLogger{
		Logger: log.Ctx(ctx),
	}
	newClient.Logger = logger
	newClient.CheckRetry = logger.CheckRetry
	return &newClient
}

type retryableClientLogger struct {
	Logger *zerolog.Logger
}

func (l *retryableClientLogger) Error(msg string, keysAndValues ...interface{}) {
	event := l.Logger.Warn()
	l.send(event, msg, keysAndValues...)
}

func (l *retryableClientLogger) Info(msg string, keysAndValues ...interface{}) {
	event := l.Logger.Info()
	l.send(event, msg, keysAndValues...)
}

func (l *retryableClientLogger) Debug(msg string, keysAndValues ...interface{}) {
	event := l.Logger.Debug()
	l.send(event, msg, keysAndValues...)
}

func (l *retryableClientLogger) Warn(msg string, keysAndValues ...interface{}) {
	event := l.Logger.Warn()
	event.Enabled()
	l.send(event, msg, keysAndValues...)
}

func (l *retryableClientLogger) send(event *zerolog.Event, msg string, keysAndValues ...interface{}) {
	if !event.Enabled() {
		return
	}
	if msg == "performing request" {
		return
	}
	for i := 0; i < len(keysAndValues); i += 2 {
		key := keysAndValues[i].(string)
		if key == "url" || key == "request" {
			continue
		}
		value := keysAndValues[i+1]
		if err, ok := value.(error); ok {
			value = RedactHttpError(err).Error()
		}
		event = event.Interface(key, value)
	}

	event.Msg(msg)
}

func (l *retryableClientLogger) CheckRetry(ctx context.Context, resp *http.Response, err error) (bool, error) {
	shouldRetry, err := retryablehttp.DefaultRetryPolicy(ctx, resp, err)
	if shouldRetry && err == nil && resp != nil {
		l.Logger.Warn().Int("statusCode", resp.StatusCode).Msg("Received retryable status code")
	}
	return shouldRetry, err
}

// If the error is a *url.Error, redact the query string values
func RedactHttpError(err error) error {
	if httpErr, ok := err.(*url.Error); ok {
		if httpErr.URL != "" {
			if index := strings.IndexByte(httpErr.URL, '?'); index != -1 {
				if u, err := url.Parse(httpErr.URL); err == nil {
					q := u.Query()
					for _, v := range q {
						for i := range v {
							v[i] = "REDACTED"
						}

					}

					u.RawQuery = q.Encode()
					httpErr.URL = u.String()
				}
			}
		}

		httpErr.Err = RedactHttpError(httpErr.Err)
	}
	return err
}

type Container struct {
	*url.URL
}

func clearBit(value int64, pos int) int64 {
	mask := int64(^(1 << pos))
	return value & mask
}

func (c *Container) GetBlobUri(blobNumber int64) string {
	// The bottom 12-bit of the blob number are used for the file number
	// After shifting the file number off, the position of the topmost bit
	// will be the root directory number. That bit is then also cleared
	// What remains of the blob number will be split into bytes and used to
	// generate the sub folders

	fileNumber := blobNumber & 0xFFF
	blobNumber = blobNumber >> 12
	rootDir := 64 - bits.LeadingZeros64(uint64(blobNumber))

	folders := []int64{}

	// Folder 00 and 01 don't have any sub folders
	if rootDir > 1 {
		blobNumber = clearBit(blobNumber, rootDir-1)

		// Work out how many sub folders there will be
		subFolderCount := ((rootDir - 2) / 8) + 1

		for i := 0; i < subFolderCount; i++ {
			folders = append(folders, blobNumber&0xFF)
			blobNumber = blobNumber >> 8
		}
	}

	// Add the root folder to the URL
	blobURL := c.URL.JoinPath(fmt.Sprintf("%02X", rootDir))

	// The folders are added to the array in reverse order, so we need to run over it back to front
	// instead of using range.
	for i := len(folders) - 1; i >= 0; i-- {
		blobURL = blobURL.JoinPath(fmt.Sprintf("%02X", folders[i]))
	}

	// and finally add the file number
	blobURL = blobURL.JoinPath(fmt.Sprintf("%03X", fileNumber))

	return blobURL.String()
}

func (c *Container) GetContainerName() string {
	return path.Base(c.Path)
}

func ValidateContainer(sasUri string, httpClient *retryablehttp.Client) (*Container, error) {
	parsedUri, err := url.Parse(sasUri)
	if err != nil {
		return nil, err
	}

	metadataUri := parsedUri.JoinPath(MetadataBlobName)

	req, err := retryablehttp.NewRequest(http.MethodGet, metadataUri.String(), nil)
	if err != nil {
		return nil, err
	}

	AddCommonBlobRequestHeaders(req.Header)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, RedactHttpError(err)
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
