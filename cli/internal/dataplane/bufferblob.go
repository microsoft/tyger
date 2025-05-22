// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

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
	"sync"
	"time"

	"github.com/microsoft/tyger/cli/internal/common"
	"github.com/rs/zerolog/log"
)

type BufferStatus string

const (
	CurrentBufferFormatVersion = "0.3.0"

	BufferStatusComplete BufferStatus = "complete"
	BufferStatusFailed   BufferStatus = "failed"

	HashChainHeader  = "x-ms-meta-cumulative_hash_chain"
	ContentMD5Header = "Content-MD5"

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

type Container struct {
	lock      *sync.Mutex
	accessUrl *url.URL
	getNewUrl func(context.Context) (*url.URL, error)
}

func NewContainer(accessUrl *url.URL) *Container {
	return &Container{accessUrl: accessUrl, lock: &sync.Mutex{}}
}

func NewContainerFromAccessString(ctx context.Context, accessString string) (*Container, error) {
	accessUrl, err := ParseBufferAccessUrl(accessString)
	if err != nil {
		// Invalid URL, assume it's a file containing the URL
		return NewContainerFromAccessFile(ctx, accessString)
	}

	// Valid URL, need to parse the SAS parameters in order to request a new URL later
	bufferId := path.Base(accessUrl.Path)
	qv := accessUrl.Query()
	permissions := qv.Get("sp")
	writeable := strings.Contains(permissions, "c")
	st, err := parseSasQueryTimestamp(accessUrl, "st")
	if err != nil {
		return nil, err
	}
	se, err := parseSasQueryTimestamp(accessUrl, "se")
	if err != nil {
		return nil, err
	}
	accessTtl := ""
	d := se.Sub(st)
	if d > 0 {
		accessTtl = common.DurationToTimeToLive(d).String()
	}

	getNewUrl := func(ctx context.Context) (*url.URL, error) {
		url, err := RequestNewBufferAccessUrl(ctx, bufferId, writeable, accessTtl)
		if err != nil {
			return nil, err
		}
		return url, nil
	}

	return newContainer(ctx, accessUrl, getNewUrl)
}

func NewContainerFromAccessFile(ctx context.Context, filename string) (*Container, error) {
	getNewUrl := func(ctx context.Context) (*url.URL, error) {
		url, err := GetBufferAccessUrlFromFile(filename)
		if err != nil {
			return nil, err
		}
		return url, nil
	}

	return newContainer(ctx, nil, getNewUrl)
}

func NewContainerFromBufferId(ctx context.Context, bufferId string, writeable bool, accessTtl string) (*Container, error) {
	getNewUrl := func(ctx context.Context) (*url.URL, error) {
		url, err := RequestNewBufferAccessUrl(ctx, bufferId, writeable, accessTtl)
		if err != nil {
			return nil, err
		}
		return url, nil
	}

	return newContainer(ctx, nil, getNewUrl)
}

func newContainer(ctx context.Context, accessUrl *url.URL, getUrl func(context.Context) (*url.URL, error)) (*Container, error) {
	if accessUrl == nil {
		url, err := getUrl(ctx)
		if err != nil {
			return nil, err
		}
		accessUrl = url
	}
	c := &Container{
		lock:      &sync.Mutex{},
		accessUrl: accessUrl,
		getNewUrl: getUrl,
	}
	return c, nil
}

// CurrentAccessUrl returns the current access URL without attempting to refresh.
func (c *Container) CurrentAccessUrl() *url.URL {
	c.lock.Lock()
	defer c.lock.Unlock()
	return c.accessUrl
}

// GetValidAccessUrl returns a valid access URL, refreshing it if necessary.
func (c *Container) GetValidAccessUrl(ctx context.Context) (*url.URL, error) {
	c.lock.Lock()
	defer c.lock.Unlock()

	// Does the URL need to be refreshed?
	refreshTime, err := calculateProactiveSasRefreshTime(c.accessUrl)
	if err != nil {
		return c.accessUrl, err
	}
	if time.Until(refreshTime) > 0 {
		return c.accessUrl, nil
	}

	// Attempt to refresh
	const MaxRetries = 5
	for retryCount := 0; retryCount < MaxRetries; retryCount++ {

		select {
		case <-ctx.Done():
			return c.accessUrl, ctx.Err()
		default:
		}

		url, err := c.getNewUrl(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get new access URL: %w", err)
		}

		c.accessUrl = url

		// Is the URL about to expire?
		expiresAt, err := parseSasQueryTimestamp(c.accessUrl, "se")
		if err != nil {
			return nil, err
		}
		expired := time.Now().Add(2 * time.Second).After(expiresAt)
		if expired {
			log.Ctx(ctx).Trace().Msgf("access URL expired, retrying refresh in %d seconds", retryCount+1)
			time.Sleep(time.Duration(retryCount+1) * time.Second)
			continue
		}

		// Good to go
		log.Ctx(ctx).Trace().Msgf("got new access URL for %s", path.Base(c.accessUrl.Path))
		return c.accessUrl, nil
	}

	return nil, fmt.Errorf("failed to get a valid access URL after %d retries", MaxRetries)
}

func parseSasQueryTimestamp(accessUrl *url.URL, key string) (time.Time, error) {
	queryString := accessUrl.Query()

	value := queryString.Get(key)
	if value == "" {
		return time.Time{}, fmt.Errorf("SAS '%s' timestamp not found in access URL %s", key, accessUrl)
	}

	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("error parsing SAS timestamp '%s': %w", value, err)
	}

	return parsed, nil
}

func calculateProactiveSasRefreshTime(u *url.URL) (time.Time, error) {
	issuedAt, err := parseSasQueryTimestamp(u, "st")
	if err != nil {
		return time.Time{}, err
	}
	expiresAt, err := parseSasQueryTimestamp(u, "se")
	if err != nil {
		return time.Time{}, err
	}

	lifetime := expiresAt.Sub(issuedAt)
	threshold := time.Duration(0.85*lifetime.Seconds()) * time.Second
	return issuedAt.Add(threshold), nil
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
	return path.Base(c.CurrentAccessUrl().Path)
}

func (c *Container) SupportsRelay() bool {
	relayParam, ok := c.CurrentAccessUrl().Query()["relay"]
	return ok && len(relayParam) == 1 && relayParam[0] == "true"
}

func (c *Container) Scheme() string {
	return c.CurrentAccessUrl().Scheme
}

func AddCommonBlobRequestHeaders(header http.Header) {
	header.Add("Date", time.Now().Format(time.RFC1123Z))
	header.Add("x-ms-version", "2021-08-06")
}

func clearBit(value int64, pos int) int64 {
	mask := int64(^(1 << pos))
	return value & mask
}
