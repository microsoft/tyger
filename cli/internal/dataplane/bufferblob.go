package dataplane

type BufferBlob struct {
	BlobName   string
	BlobNumber int64
	Contents   []byte

	// For Writing
	PreviousCumulativeHash chan string
	CurrentCumulativeHash  chan string

	// For Reading
	EncodedMD5Hash      string
	EncodedMD5ChainHash string
}

type BufferFormat struct {
	Version string `json:"version"`
}

type BufferFinalization struct {
	Status string `json:"status"`
}
