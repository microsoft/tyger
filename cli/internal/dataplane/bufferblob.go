package dataplane

var (
	BufferVersion string = "0.1.0"
)

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
	LastError           error
}

type BufferFormat struct {
	Version string `json:"version"`
}

type BufferFinalization struct {
	BlobCount int64  `json:"blobCount"`
	Status    string `json:"status"`
}
