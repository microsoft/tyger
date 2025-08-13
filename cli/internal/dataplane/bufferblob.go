// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

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
	"sync"
	"time"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/rs/zerolog/log"
)

type BufferStatus string

const (
	CurrentBufferFormatVersion = "0.3.0"

	BufferStatusComplete BufferStatus = "complete"
	BufferStatusFailed   BufferStatus = "failed"

	HashChainHeader  = "x-ms-meta-cumulative_hash_chain"
	ContentMD5Header = "Content-MD5"
	ErrorCodeHeader  = "x-ms-error-code"

	StartMetadataBlobName = ".bufferstart"
	EndMetadataBlobName   = ".bufferend"
)

var (
	errMd5Mismatch        = errors.New("MD5 mismatch")
	errBufferDoesNotExist = errors.New("the buffer does not exist")
	errServerBusy         = errors.New("server is busy")
	errOperationTimeout   = errors.New("operation timeout")
)

type BufferBlob struct {
	BlobNumber int64
	Contents   []byte
	Error      error

	// For Writing
	PreviousCumulativeHash chan string
	CurrentCumulativeHash  chan string

	// For Reading
	EncodedMD5Hash      string
	EncodedMD5ChainHash string
}

type BufferStartMetadata struct {
	Version string `json:"version"`
}

type BufferEndMetadata struct {
	Status BufferStatus `json:"status"`
}

type InvalidAccessUrlError struct {
	Reason string
}

func (e *InvalidAccessUrlError) Error() string {
	if e.Reason == "" {
		return "invalid access URL"
	}

	return fmt.Sprintf("invalid access URL: %s", e.Reason)
}

type Container struct {
	initialAccessUrl *url.URL
	getNewUrl        func(context.Context) (*url.URL, error)
}

func NewContainer(accessUrl *url.URL) *Container {
	return &Container{
		initialAccessUrl: accessUrl,
		getNewUrl:        nil,
	}
}

func NewContainerFromAccessString(ctx context.Context, accessString string) (*Container, error) {
	accessUrl, err := ParseBufferAccessUrl(accessString)
	if err != nil {
		// Invalid URL, assume it's a file containing the URL
		return NewContainerFromAccessFile(ctx, accessString)
	}

	// We don't refresh if a SAS URL is provided - the user may not be logged into Tyger
	return newContainer(ctx, accessUrl, nil)
}

func NewContainerFromAccessFile(ctx context.Context, filename string) (*Container, error) {
	const RetryCount = 10
	var lastUrl *url.URL
	getNewUrl := func(ctx context.Context) (*url.URL, error) {
		for retryCount := range 10 {
			newUrl, err := GetBufferAccessUrlFromFile(filename)
			if err != nil {
				return nil, err
			}

			if lastUrl == nil || lastUrl.String() != newUrl.String() {
				lastUrl = newUrl

				return newUrl, nil
			}

			log.Ctx(ctx).Warn().Msgf("Access URL from file %s has not changed, attempt %d/%d", filename, retryCount+1, RetryCount)
			select {
			case <-time.After(30 * time.Second):
				// Continue to next iteration
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		return nil, fmt.Errorf("access URL from file %s has not changed after %d attempts", filename, RetryCount)
	}

	return newContainer(ctx, nil, getNewUrl)
}

func NewContainerFromBufferId(ctx context.Context, bufferId string, writeable bool, accessTtl string) (*Container, error) {
	getNewUrl := func(ctx context.Context) (*url.URL, error) {
		const MaxRetries = 20
		for attempt := range MaxRetries {
			u, err := RequestNewBufferAccessUrl(ctx, bufferId, writeable, accessTtl)
			if err != nil {
				log.Ctx(ctx).Warn().Msgf("Failed to get new buffer access URL (attempt %d/%d): %v", attempt+1, MaxRetries, err)
				time.Sleep(5 * time.Second)
				continue
			}

			return u, nil
		}

		return nil, fmt.Errorf("failed to get new buffer access URL after %d attempts", MaxRetries)
	}

	return newContainer(ctx, nil, getNewUrl)
}

func newContainer(ctx context.Context, accessUrl *url.URL, getUrl func(context.Context) (*url.URL, error)) (*Container, error) {
	if accessUrl == nil {
		if getUrl == nil {
			return nil, fmt.Errorf("no access URL or function to get a new URL provided")
		}
		url, err := getUrl(ctx)
		if err != nil {
			return nil, err
		}
		accessUrl = url
	}
	c := &Container{
		initialAccessUrl: accessUrl,
		getNewUrl:        getUrl,
	}
	return c, nil
}

func (c *Container) NewContainerClient(httpClient *retryablehttp.Client) *ContainerClient {
	baseUrl := *c.initialAccessUrl
	baseUrl.RawQuery = ""

	return &ContainerClient{
		innerClient:           httpClient,
		baseUrl:               &baseUrl,
		currentAccessUrl:      c.initialAccessUrl,
		currentAccessUrlQuery: c.initialAccessUrl.Query(),
		getNewAccessUrl:       c.getNewUrl,
		mutex:                 sync.RWMutex{},
	}
}

func MakeBlobPath(blobNumber int64) string {
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
	return fmt.Sprintf("%s/%03X", builder.String(), fileNumber)
}

func (c *Container) GetContainerName() string {
	return path.Base(c.initialAccessUrl.Path)
}

func (c *Container) SupportsRelay() bool {
	relayParam, ok := c.initialAccessUrl.Query()["relay"]
	return ok && len(relayParam) == 1 && relayParam[0] == "true"
}

func (c *Container) Scheme() string {
	return c.initialAccessUrl.Scheme
}

func AddCommonBlobRequestHeaders(header http.Header) {
	header.Add("Date", time.Now().Format(time.RFC1123Z))
	header.Add("x-ms-version", "2021-08-06")
}

func clearBit(value int64, pos int) int64 {
	mask := int64(^(1 << pos))
	return value & mask
}

type ContainerClient struct {
	innerClient     *retryablehttp.Client
	baseUrl         *url.URL
	getNewAccessUrl func(context.Context) (*url.URL, error)
	mutex           sync.RWMutex

	// all remaining fields require the mutex to access
	currentAccessUrl      *url.URL
	currentAccessUrlQuery url.Values
	accessUrlGen          int64
	refreshError          error
}

func (c *ContainerClient) NewRequestWithRelativeUrl(ctx context.Context, method string, relativeUrl string, body any) *retryablehttp.Request {
	req, err := retryablehttp.NewRequestWithContext(ctx, method, c.baseUrl.JoinPath(relativeUrl).String(), body)
	if err != nil {
		panic(fmt.Sprintf("failed to create request: %v", err))
	}

	return req
}

func (c *ContainerClient) NewNonRetryableRequestWithRelativeUrl(ctx context.Context, method string, relativeUrl string, body io.Reader) *http.Request {
	req, err := http.NewRequestWithContext(ctx, method, c.baseUrl.JoinPath(relativeUrl).String(), body)
	if err != nil {
		panic(fmt.Sprintf("failed to create request: %v", err))
	}

	return req
}

func (c *ContainerClient) Do(req *retryablehttp.Request) (*http.Response, error) {
	initialGen := c.updateRequestUrl(req.Request)

	resp, err := c.innerClient.Do(req)

	if c.getNewAccessUrl == nil ||
		err != nil ||
		resp.StatusCode != http.StatusForbidden ||
		(resp.Header.Get(ErrorCodeHeader) != "AuthenticationFailed") {
		return resp, err
	}

	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	err = func() error {
		c.mutex.Lock()
		defer c.mutex.Unlock()
		if c.refreshError != nil {
			return c.refreshError
		}

		if c.accessUrlGen != initialGen {
			return nil // The URL has already been refreshed, no need to do it again
		}

		log.Ctx(req.Context()).Info().Msg("Refreshing acccess URL")

		newUrl, err := c.getNewAccessUrl(req.Context())
		if err != nil {
			c.refreshError = fmt.Errorf("failed to refresh access URL")
			return c.refreshError
		}

		log.Ctx(req.Context()).Info().Msg("Updated access URL")

		c.currentAccessUrl = newUrl
		c.currentAccessUrlQuery = newUrl.Query()
		c.accessUrlGen++
		return nil
	}()

	if err != nil {
		return nil, fmt.Errorf("failed to refresh access URL: %w", err)
	}

	c.updateRequestUrl(req.Request)
	return c.innerClient.Do(req)
}

func (c *ContainerClient) updateRequestUrl(req *http.Request) int64 {
	if c.getNewAccessUrl != nil {
		c.mutex.RLock()
		defer c.mutex.RUnlock()
	}

	if c.currentAccessUrl == nil {
		panic("currentAccessUrl is nil, cannot update request URL")
	}

	if req.URL.RawQuery == "" {
		req.URL.RawQuery = c.currentAccessUrl.RawQuery
		return c.accessUrlGen
	}
	reqQuery := req.URL.Query()
	for k, v := range c.currentAccessUrlQuery {
		reqQuery[k] = v
	}
	req.URL.RawQuery = reqQuery.Encode()
	return c.accessUrlGen
}
