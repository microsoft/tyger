package dataplane

import (
	"bytes"
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCopyToPipeFullReads(t *testing.T) {
	inputBuf := make([]byte, 0, 64*1024*3)
	fullInput := inputBuf[:cap(inputBuf)]
	input := bytes.NewBuffer(inputBuf)
	require.NoError(t, Gen(int64(input.Cap()), input))

	output := &bytes.Buffer{}
	require.NoError(t, copyToPipe(output, input))
	require.True(t, bytes.Equal(fullInput, output.Bytes()))
}

func TestCopyToPipeLimitedChunkSizes(t *testing.T) {
	testCases := []struct{ n int }{
		{8 * 1024},
		{32*1024 - 1},
		{32 * 1024},
		{32*1024 + 1},
	}
	for _, tC := range testCases {
		t.Run(fmt.Sprintf("%d", tC.n), func(t *testing.T) {
			inputBuf := make([]byte, 0, 64*1024*3)
			fullInput := inputBuf[:cap(inputBuf)]
			input := bytes.NewBuffer(inputBuf)
			require.NoError(t, Gen(int64(input.Cap()), input))

			output := &bytes.Buffer{}
			require.NoError(t, copyToPipe(output, &MaxChunkReader{input, tC.n}))
			require.True(t, bytes.Equal(fullInput, output.Bytes()))
		})
	}
}

type MaxChunkReader struct {
	io.Reader
	maxChunk int
}

func (r *MaxChunkReader) Read(p []byte) (n int, err error) {
	if len(p) > r.maxChunk {
		p = p[:r.maxChunk]
	}

	return r.Reader.Read(p)
}
