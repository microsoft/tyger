package dataplane

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
