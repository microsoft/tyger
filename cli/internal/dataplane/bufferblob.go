package dataplane

import (
	"errors"
)

const (
	CurrentBufferFormatVersion = "0.2.0"

	BufferStatusComplete = "complete"
	BufferStatusFailed   = "failed"

	HashChainHeader  = "x-ms-meta-cumulative_md5_chain"
	ContentMD5Header = "Content-MD5"

	StartMetadataBlobName = ".bufferstart"
	EndMetadataBlobName   = ".bufferend"
)

var (
	errMd5Mismatch        = errors.New("MD5 mismatch")
	errBufferDoesNotExist = errors.New("the buffer does not exist")
)

type BufferBlob struct {
	BlobNumber int64
	Contents   []byte

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
	Status string `json:"status"`
}
