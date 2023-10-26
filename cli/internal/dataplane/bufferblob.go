package dataplane

import (
	"fmt"
)

const (
	CurrentBufferFormatVersion = "0.1.0"

	BufferStatusComplete = "complete"
	BufferStatusFailed   = "failed"

	HashChainHeader = "x-ms-meta-cumulative_md5_chain"

	StartMetadataBlobName = ".bufferstart"
	EndMetadataBlobName   = ".bufferend"
)

var (
	errMd5Mismatch        = fmt.Errorf("MD5 mismatch")
	errBufferDoesNotExist = fmt.Errorf("the buffer does not exist")
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
