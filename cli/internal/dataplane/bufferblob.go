package dataplane

type BufferBlob struct {
	BlobNumber          int64
	Contents            []byte
	EncodedMD5Hash      string
	EncodedMD5ChainHash string
}
