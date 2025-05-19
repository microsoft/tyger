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
	lock        *sync.Mutex
	accessUrl   *url.URL
	refreshTime time.Time
	ctx         context.Context
	getNewUrl   func(context.Context) (*url.URL, error)
}

func NewContainer(accessUrl *url.URL) *Container {
	return &Container{accessUrl: accessUrl}
}

func NewContainerFromFile(ctx context.Context, filename string) (*Container, error) {
	getNewUrl := func(ctx context.Context) (*url.URL, error) {
		url, err := GetUrlFromAccessString(filename)
		if err != nil {
			return nil, err
		}
		return url, nil
	}

	accessUrl, err := getNewUrl(ctx)
	if err != nil {
		return nil, err
	}

	c := &Container{
		accessUrl: accessUrl,
		lock:      &sync.Mutex{},
		ctx:       ctx,
		getNewUrl: getNewUrl,
	}

	return c, nil
}

func NewContainerFromBufferId(ctx context.Context, accessString string, writeable bool, accessTtl string) (*Container, error) {
	getNewUrl := func(ctx context.Context) (*url.URL, error) {
		url, err := GetNewBufferAccessUrl(ctx, accessString, writeable, accessTtl)
		if err != nil {
			return nil, err
		}
		return url, nil
	}

	accessUrl, err := getNewUrl(ctx)
	if err != nil {
		return nil, err
	}

	refreshTime, err := getSasRefreshTime(accessUrl)
	if err != nil {
		return nil, err
	}

	c := &Container{
		lock:        &sync.Mutex{},
		accessUrl:   accessUrl,
		refreshTime: refreshTime,
		ctx:         ctx,
		getNewUrl:   getNewUrl,
	}

	return c, nil
}

func (c *Container) SetContext(ctx context.Context) {
	c.ctx = ctx
}

func (c *Container) GetAccessUrl() *url.URL {
	if c.lock == nil {
		return c.accessUrl
	}

	c.lock.Lock()
	defer c.lock.Unlock()

	retryCount := 0
	for retryCount = 0; retryCount < 5; retryCount++ {

		if time.Until(c.refreshTime) > 0 {
			return c.accessUrl
		}

		url, err := c.getNewUrl(c.ctx)
		if err != nil {
			log.Ctx(c.ctx).Error().Err(err).Msg("failed to get new access URL")
		} else {
			c.accessUrl = url
			refreshTime, err := getSasRefreshTime(c.accessUrl)
			if err != nil {
				log.Ctx(c.ctx).Error().Err(err).Msg("failed to compute access URL refresh time")
			} else {
				c.refreshTime = refreshTime
				if time.Until(c.refreshTime) > 0 {
					return c.accessUrl
				}
			}
		}

		time.Sleep(time.Duration(retryCount+1) * time.Second)
	}

	log.Ctx(c.ctx).Error().Msgf("failed to get a valid access URL after %d retries", retryCount)

	return c.accessUrl
}

func getSasRefreshTime(u *url.URL) (time.Time, error) {
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

func (c *Container) JoinPath(pathElements ...string) string {
	return c.GetAccessUrl().JoinPath(pathElements...).String()
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
	return path.Base(c.GetAccessUrl().Path)
}

func (c *Container) SupportsRelay() bool {
	relayParam, ok := c.GetAccessUrl().Query()["relay"]
	return ok && len(relayParam) == 1 && relayParam[0] == "true"
}

func (c *Container) Scheme() string {
	return c.GetAccessUrl().Scheme
}

func AddCommonBlobRequestHeaders(header http.Header) {
	header.Add("Date", time.Now().Format(time.RFC1123Z))
	header.Add("x-ms-version", "2021-08-06")
}

func clearBit(value int64, pos int) int64 {
	mask := int64(^(1 << pos))
	return value & mask
}
