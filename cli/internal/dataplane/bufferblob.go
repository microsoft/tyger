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
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
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
	accessUrl  *url.URL
	lock       *sync.Mutex
	autoUpdate func(context.Context, *sync.WaitGroup)
}

func NewContainer(accessUrl *url.URL) *Container {
	return &Container{accessUrl: accessUrl}
}

func NewContainerFromFile(ctx context.Context, filename string) (*Container, error) {
	accessUrl, err := GetUrlFromAccessString(filename)
	if err != nil {
		return nil, err
	}

	c := &Container{
		accessUrl: accessUrl,
		lock:      &sync.Mutex{},
	}
	c.autoUpdate = func(ctx context.Context, wg *sync.WaitGroup) {
		c.autoUpdateFromFile(ctx, wg, filename)
	}

	return c, nil
}

func NewContainerFromBufferId(ctx context.Context, accessString string, writeable bool, accessTtl string) (*Container, error) {
	getNewUrl := func(ctx context.Context) (*url.URL, error) {
		url, err := GetNewBufferAccessUrl(ctx, accessString, writeable, accessTtl)
		if err != nil {
			return nil, fmt.Errorf("unable to get read access to buffer: %w", err)
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
	}
	c.autoUpdate = func(ctx context.Context, wg *sync.WaitGroup) {
		c.autoUpdateWithClient(ctx, wg, getNewUrl)
	}

	return c, nil
}

func (c *Container) StartAutoRefresh(ctx context.Context) {
	if c.autoUpdate != nil {
		wg := &sync.WaitGroup{}
		wg.Add(1)
		go c.autoUpdate(ctx, wg)
		wg.Wait()
	}
}

func (c *Container) autoUpdateWithClient(ctx context.Context, wg *sync.WaitGroup, getNewUrl func(context.Context) (*url.URL, error)) {
	if c.accessUrl == nil {
		panic("accessUrl is nil")
	}

	getTimeUntilRefresh := func(u *url.URL) (time.Duration, error) {
		expiresAt, err := getSasExpirationTime(u)
		if err != nil {
			return 0, err
		}

		timeUntilExpiration := time.Until(expiresAt)
		var timeUntilRefresh time.Duration
		if timeUntilExpiration > 0 {
			timeUntilRefresh = time.Duration(timeUntilExpiration.Seconds()*0.75) * time.Second
		}

		return timeUntilRefresh, nil
	}

	timeUntilRefresh, err := getTimeUntilRefresh(c.accessUrl)
	if err != nil {
		log.Ctx(ctx).Error().Err(err).Msg("container auto-refresh error")
		return
	}

	finished := make(chan bool)
	go func() {
		defer func() {
			finished <- true
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(timeUntilRefresh):
				url, err := getNewUrl(ctx)
				if err != nil {
					log.Ctx(ctx).Error().Err(err).Msg("container auto-refresh error")
					return
				}

				c.setAccessUrl(ctx, url)

				timeUntilRefresh, err = getTimeUntilRefresh(c.accessUrl)
				if err != nil {
					log.Ctx(ctx).Error().Err(err).Msg("container auto-refresh error")
					return
				}
			}
		}
	}()
	wg.Done()
	<-finished
}

func (c *Container) autoUpdateFromFile(ctx context.Context, wg *sync.WaitGroup, sasFilePath string) {
	// Read the file to ensure we're not starting with an expired URL
	url, err := GetUrlFromAccessString(sasFilePath)
	if err != nil {
		log.Ctx(ctx).Error().Err(err).Msg("container auto-refresh error")
		return
	}
	c.setAccessUrl(ctx, url)

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		err = fmt.Errorf("failed to create file watcher: %w", err)
		log.Ctx(ctx).Error().Err(err).Msg("container auto-refresh error")
		return
	}
	defer watcher.Close()

	sasFile := filepath.Clean(sasFilePath)
	sasDir, _ := filepath.Split(sasFile)
	realSasFile, _ := filepath.EvalSymlinks(sasFilePath)

	finished := make(chan bool)
	go func() {
		defer func() {
			finished <- true
		}()

		for {
			select {
			case <-ctx.Done():
				return

			case event, ok := <-watcher.Events:
				if !ok {
					log.Ctx(ctx).Debug().Msg("container auto-refresh file watcher closed")
					return
				}

				currentSasFile, _ := filepath.EvalSymlinks(sasFilePath)
				fileWasModified := filepath.Clean(event.Name) == sasFile && (event.Has(fsnotify.Write) || event.Has(fsnotify.Create))
				fileMoved := currentSasFile != "" && currentSasFile != realSasFile

				if fileWasModified || fileMoved {
					realSasFile = currentSasFile
					watcher.Add(sasFile)

					url, err := GetUrlFromAccessString(sasFilePath)
					if err != nil {
						log.Ctx(ctx).Error().Err(err).Msg("container auto-refresh error")
						return
					}

					c.setAccessUrl(ctx, url)
				}

			case err, ok := <-watcher.Errors:
				if !ok {
					log.Ctx(ctx).Debug().Msg("container auto-refresh file watcher closed")
					return
				}
				err = fmt.Errorf("file watcher error: %w", err)
				log.Ctx(ctx).Error().Err(err).Msg("container auto-refresh error")
				return
			}
		}
	}()

	watcher.Add(sasDir)
	watcher.Add(sasFile)

	wg.Done()
	<-finished
}

func (c *Container) GetAccessUrl() *url.URL {
	if c.lock != nil {
		c.lock.Lock()
		defer c.lock.Unlock()
	}
	return c.accessUrl
}

func (c *Container) setAccessUrl(ctx context.Context, accessUrl *url.URL) {
	if c.lock != nil {
		c.lock.Lock()
	}

	c.accessUrl = accessUrl

	if c.lock != nil {
		c.lock.Unlock()
	}

	se, _ := getSasExpirationTime(accessUrl)
	log.Ctx(ctx).Debug().Msgf("New access URL for %s expires at %s", c.GetContainerName(), se.String())
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
