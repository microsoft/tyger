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

type AccessUrlUpdateFunc func(ctx context.Context, writeable bool) (*url.URL, error)

type Container struct {
	accessUrl *url.URL

	ctx context.Context

	lock *sync.Mutex

	refresher AccessUrlUpdateFunc
}

func clearBit(value int64, pos int) int64 {
	mask := int64(^(1 << pos))
	return value & mask
}

func NewContainer(ctx context.Context, accessUrl *url.URL, refresher AccessUrlUpdateFunc) *Container {
	return &Container{
		accessUrl: accessUrl,
		ctx:       ctx,
		lock:      &sync.Mutex{},
		refresher: refresher,
	}
}

func (c *Container) GetAccessUrl() *url.URL {
	c.lock.Lock()
	defer c.lock.Unlock()

	if c.accessUrl == nil {
		return nil
	}

	if c.refresher == nil {
		panic("refresher is nil")
		return c.accessUrl
	}

	// Parse the query parameters to determine the SAS expiration time
	queryString := c.accessUrl.Query()

	se := queryString.Get("se")
	if se == "" {
		log.Ctx(c.ctx).Debug().Msgf("SAS expiration not found in access URL %s", c.accessUrl)
		return c.accessUrl
	}

	// TODO: OK to swallow this error?
	seParsed, err := time.Parse(time.RFC3339, se)
	if err != nil {
		log.Ctx(c.ctx).Error().Err(err).Msgf("Error parsing SAS expiration time %s", se)
		return c.accessUrl
	}

	timeToRefresh := seParsed.Add(-time.Second * 2)
	now := time.Now().UTC()

	if now.After(timeToRefresh) {
		sp := queryString.Get("sp")
		if !strings.Contains(sp, "r") {
			log.Ctx(c.ctx).Error().Msgf("SAS token does not contain read permission %s", c.accessUrl)
			return c.accessUrl
		}
		writeable := strings.Contains(sp, "c")

		for retryCount := 0; retryCount < 5; retryCount++ {
			log.Ctx(c.ctx).Debug().Msgf("Refreshing access URL for %s", c.GetContainerName())
			url, err := c.refresher(c.ctx, writeable)
			if err != nil || url == nil {
				// TODO: Should surface this error
				log.Ctx(c.ctx).Error().Err(err).Msgf("Error refreshing access URL for %s", c.GetContainerName())
				return c.accessUrl
			}

			if url.String() == c.accessUrl.String() {
				log.Ctx(c.ctx).Warn().Msgf("JOE: Access URL for %s is the same as before, not updated", c.GetContainerName())
				time.Sleep(time.Duration(retryCount+1) * time.Second)
			} else {
				log.Ctx(c.ctx).Debug().Msgf("JOE: Access URL for %s updated successfully", c.GetContainerName())
				c.accessUrl = url
				return c.accessUrl
			}
		}
		log.Ctx(c.ctx).Error().Err(err).Msgf("Failed to refresh access URL for %s", c.GetContainerName())
	} else {
		// log.Ctx(c.ctx).Debug().Msgf("JOE: Access URL for %s is still valid (expiration %s > %s)", c.GetContainerName(), seParsed.String(), now.String())
	}

	return c.accessUrl
}

func (c *Container) GetBlobUrl(blobNumber int64) string {
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
	blobURL := c.GetAccessUrl().JoinPath(builder.String(), fmt.Sprintf("%03X", fileNumber))

	return blobURL.String()
}

func (c *Container) GetStartMetadataUrl() string {
	return c.GetAccessUrl().JoinPath(StartMetadataBlobName).String()
}

func (c *Container) GetEndMetadataUrl() string {
	return c.GetAccessUrl().JoinPath(EndMetadataBlobName).String()
}

func (c *Container) GetContainerName() string {
	return path.Base(c.accessUrl.Path)
}

func (c *Container) SupportsRelay() bool {
	relayParam, ok := c.accessUrl.Query()["relay"]
	return ok && len(relayParam) == 1 && relayParam[0] == "true"
}

func AddCommonBlobRequestHeaders(header http.Header) {
	header.Add("Date", time.Now().Format(time.RFC1123Z))
	header.Add("x-ms-version", "2021-08-06")
}
