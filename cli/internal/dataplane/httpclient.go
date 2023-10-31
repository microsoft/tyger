package dataplane

import (
	"context"
	"errors"
	"fmt"
	"math/bits"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/microsoft/tyger/cli/internal/httpclient"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

const (
	MaxRetries    = 6
	RetryDelay    = 800 * time.Millisecond
	MaxRetryDelay = 30 * time.Second
)

type responseBodyReadError struct {
	reason error
}

func (e *responseBodyReadError) Error() string {
	return fmt.Sprintf("error reading response body: %v", e.reason)
}

func (e *responseBodyReadError) Unwrap() error {
	return e.reason
}

func CreateHttpClient(ctx context.Context, proxyUri string) (*retryablehttp.Client, error) {
	client := retryablehttp.NewClient()
	client.RetryMax = 6
	client.HTTPClient.Timeout = 100 * time.Second

	logger := &retryableClientLogger{
		Logger: &log.Logger,
		Ctx:    ctx,
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
	} else {
		transport.Proxy = httpclient.GetProxyFunc()
	}

	return client, nil
}

// Performs a shallow clone of the retryable client adjusting the logger
// to capture the context.
// The inner http.Client is reused.
func NewClientWithLoggingContext(ctx context.Context, client *retryablehttp.Client) *retryablehttp.Client {
	logger := &retryableClientLogger{
		Logger: log.Ctx(ctx),
		Ctx:    ctx,
	}

	return &retryablehttp.Client{
		Logger:          logger,
		CheckRetry:      logger.CheckRetry,
		HTTPClient:      client.HTTPClient,
		RetryWaitMin:    client.RetryWaitMin,
		RetryWaitMax:    client.RetryWaitMax,
		RetryMax:        client.RetryMax,
		RequestLogHook:  client.RequestLogHook,
		ResponseLogHook: client.ResponseLogHook,
		Backoff:         client.Backoff,
		ErrorHandler:    client.ErrorHandler,
	}
}

type retryableClientLogger struct {
	Logger *zerolog.Logger
	Ctx    context.Context
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
			if errors.Is(err, l.Ctx.Err()) {
				// If the context is canceled, don't log here.
				// The error will logged higher up.
				return
			}
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
	// We are adopting this logic because:
	// * The alphabetical sorting of the blob name corresponds to the numerical
	//   ordering of the blob number.
	// * Directories contain a maximum number of files (ideally fewer than 5000, as
	//   that is the maximum number returned in a List Blobs response page).
	// * We should be able to manage huge buffers, allowing us to store at least
	//   petabyte-sized buffers.
	// * The path should not be excessively long for small buffers.
	//
	// The bottom 12-bit of the blob number are used for the file number
	// After shifting the file number off, the position of the topmost bit
	// will be the root directory number. That bit is then also cleared
	// What remains of the blob number will be split into bytes and used to
	// generate the sub folders
	//
	// ie: blob 0x0000 = 00/000
	//          0x0400 = 00/400
	//		    0x2010 = 02/00/010
	//          0x5321 = 03/01/321
	//			0x10102345 = 11/01/02/345
	//

	fileNumber := blobNumber & 0xFFF
	blobNumber = blobNumber >> 12
	rootDir := 64 - bits.LeadingZeros64(uint64(blobNumber))

	var folders []int64

	// Folder 00 and 01 don't have any sub folders
	if rootDir > 1 {
		blobNumber = clearBit(blobNumber, rootDir-1)

		// Work out how many sub folders there will be
		subFolderCount := ((rootDir - 2) / 8) + 1

		folders = make([]int64, subFolderCount)

		for i := 0; i < subFolderCount; i++ {
			folders[i] = blobNumber & 0xFF
			blobNumber = blobNumber >> 8
		}
	}

	// Add the root folder to the URL
	builder := strings.Builder{}
	builder.WriteString(fmt.Sprintf("%02X", rootDir))

	// The folders are added to the array in reverse order, so we need to run over it back to front
	// instead of using range.
	for i := len(folders) - 1; i >= 0; i-- {
		builder.WriteString(fmt.Sprintf("/%02X", folders[i]))
	}

	// and finally add the file number
	blobURL := c.URL.JoinPath(builder.String(), fmt.Sprintf("%03X", fileNumber))

	return blobURL.String()
}

func (c *Container) GetStartMetadataUri() string {
	return c.URL.JoinPath(StartMetadataBlobName).String()
}

func (c *Container) GetEndMetadataUri() string {
	return c.URL.JoinPath(EndMetadataBlobName).String()
}

func (c *Container) GetContainerName() string {
	return path.Base(c.Path)
}

func NewContainer(sasUri string, httpClient *retryablehttp.Client) (*Container, error) {
	parsedUri, err := url.Parse(sasUri)
	if err != nil {
		return nil, err
	}

	return &Container{parsedUri}, nil
}

func AddCommonBlobRequestHeaders(header http.Header) {
	header.Add("Date", time.Now().Format(time.RFC1123Z))
	header.Add("x-ms-version", "2021-08-06")
}
