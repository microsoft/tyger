// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package dataplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"testing"
	"time"

	pool "github.com/libp2p/go-buffer-pool"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/require"
	"github.com/sunshineplan/limiter"
)

func init() {
	log.Logger = log.Logger.Level(zerolog.ErrorLevel)
}

const (
	KiB = 1024
	MiB = 1024 * KiB
	GiB = 1024 * MiB
)

func TestReadInBlocksWithMaximumIntervalFullSpeed(t *testing.T) {
	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		Gen(32*MiB, pw)
	}()

	blockSequence := readInBlocksWithMaximumInterval(context.Background(), pr, 4*MiB+7, time.Second)
	hasher := sha256.New()
	for block, err := range blockSequence {
		require.NoError(t, err)
		_, err = hasher.Write(block)
		require.NoError(t, err)
		pool.Put(block)
	}

	require.Equal(t, "b997630f9de63d635b4f686f6e4d9827d253bcf3e0ee204186095486ff64793c", hex.EncodeToString(hasher.Sum(nil)))
}

func TestReadInBlocksWithMaximumIntervalThrottled(t *testing.T) {
	pr, pw := io.Pipe()
	go func() {
		defer pw.Close()
		limitWriter := limiter.New(1 * MiB).Writer(pw)
		Gen(3*MiB, limitWriter)
	}()

	blockSequence := readInBlocksWithMaximumInterval(context.Background(), pr, 4*MiB+21, time.Millisecond)
	hasher := sha256.New()
	for block, err := range blockSequence {
		require.NoError(t, err)
		_, err = hasher.Write(block)
		require.NoError(t, err)
		pool.Put(block)
	}

	require.Equal(t, "09d7daed99c15bcee48dfa269c7597a1036a371c90100dda6f4fdcfe7ab79385", hex.EncodeToString(hasher.Sum(nil)))
}
