// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package dataplane

import (
	"bytes"
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPartiallyBufferedReaderReadWithoutRewinding(t *testing.T) {
	inputBytes := make([]byte, 2048)
	for i := range inputBytes {
		inputBytes[i] = byte(i % 256)
	}

	testCases := []struct {
		capacity int
		copySize int
	}{
		{1, 1},
		{4, 4},
		{16, 4},
		{16, 32},
		{7, 3},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("%d:%d", tc.capacity, tc.copySize), func(t *testing.T) {
			inputReader := bytes.NewReader(inputBytes)

			bufReader := NewPartiallyBufferedReader(inputReader, tc.capacity)
			outputBuffer := &bytes.Buffer{}
			unoptimizedCopy(outputBuffer, bufReader, tc.copySize)

			require.True(t, bytes.Equal(inputBytes, outputBuffer.Bytes()))
		})
	}
}

func TestPartiallyBufferedReaderBasicRewind(t *testing.T) {
	inputBytes := make([]byte, 128)
	for i := range inputBytes {
		inputBytes[i] = byte(i)
	}

	testCases := []struct {
		capacity        int
		initialReadSize int
		copySize        int
	}{
		{128, 128, 128},
		{128, 127, 512},
		{128, 127, 126},
		{128, 129, 512},
		{32, 32, 32},
		{32, 31, 32},
		{32, 31, 22},
		{32, 31, 22},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("%d:%d:%d", tc.capacity, tc.initialReadSize, tc.copySize), func(t *testing.T) {
			inputReader := bytes.NewReader(inputBytes)

			bufReader := NewPartiallyBufferedReader(inputReader, tc.capacity)

			b := make([]byte, tc.initialReadSize)

			n, err := bufReader.Read(b)
			require.NoError(t, err)
			require.Equal(t, min(len(b), len(inputBytes)), n)

			require.NoError(t, bufReader.Rewind())
			out := bytes.Buffer{}
			unoptimizedCopy(&out, bufReader, tc.copySize)

			require.True(t, bytes.Equal(inputBytes, out.Bytes()))
		})
	}

}
func TestPartiallyBufferedReaderCannotRewind(t *testing.T) {
	inputBytes := make([]byte, 128)
	for i := range inputBytes {
		inputBytes[i] = byte(i)
	}

	testCases := []struct {
		capacity        int
		initialReadSize int
	}{
		{64, 65},
		{64, 129},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("%d:%d", tc.capacity, tc.initialReadSize), func(t *testing.T) {
			inputReader := bytes.NewReader(inputBytes)

			bufReader := NewPartiallyBufferedReader(inputReader, tc.capacity)

			b := make([]byte, tc.initialReadSize)
			n, err := bufReader.Read(b)
			require.NoError(t, err)
			require.Equal(t, min(len(b), len(inputBytes)), n)

			require.Error(t, bufReader.Rewind())
		})
	}
}

// Like io.Copy but without any optimizations
func unoptimizedCopy(dst io.Writer, src io.Reader, bufferSize int) error {
	buf := make([]byte, bufferSize)
	_, err := io.CopyBuffer(&simpleWriter{dst}, &simpleReader{src}, buf)
	return err
}

func Test(t *testing.T) {
	var b io.Reader = &simpleReader{&bytes.Buffer{}}

	if _, ok := b.(io.WriterTo); ok {
		require.FailNow(t, "b is a WriterTo")
	}
}

// Hides io.WriterTo implementation
type simpleReader struct {
	io.Reader
}

// Hides io.ReaderFrom implementation
type simpleWriter struct {
	io.Writer
}
