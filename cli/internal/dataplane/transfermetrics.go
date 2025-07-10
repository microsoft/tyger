// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package dataplane

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/rs/zerolog/log"
)

type TransferMetrics struct {
	ctx                context.Context
	started            atomic.Bool
	totalBuffers       atomic.Uint64
	totalBytes         atomic.Uint64
	currentPeriodBytes atomic.Uint64
	startTime          time.Time
	ticker             *time.Ticker
	stoppedChannel     chan any
	reportingComplete  chan any
	mut                sync.Mutex
}

func NewTransferMetrics(ctx context.Context) *TransferMetrics {
	return &TransferMetrics{
		ctx: ctx,
	}
}

// Called when a buffer or buffer have been completely transferred
func (ts *TransferMetrics) UpdateCompleted(byteCount uint64, completedBuffers uint64) {
	ts.totalBytes.Add(byteCount)
	ts.totalBuffers.Add(completedBuffers)
}

// Called when data HTTP body is being read or written.
// Note that because of retries, this
func (ts *TransferMetrics) UpdateInFlight(byteCount uint64) {
	ts.currentPeriodBytes.Add(byteCount)
}

func (ts *TransferMetrics) EnsureStarted(startTime *time.Time) {
	if ts.started.Swap(true) {
		if startTime != nil {
			// Update the start time if it is earlier than the current one.
			// This can happen when that was downloaded was started before a one that completed first.
			ts.mut.Lock()
			defer ts.mut.Unlock()
			if startTime.Before(ts.startTime) {
				ts.startTime = *startTime
			}

			ts.startTime = *startTime
		}
		return
	}

	ts.stoppedChannel = make(chan any)
	ts.reportingComplete = make(chan any)
	lastTime := time.Now()
	ts.mut.Lock()
	defer ts.mut.Unlock()
	if startTime != nil {
		ts.startTime = *startTime
	} else {
		ts.startTime = lastTime
	}

	ts.ticker = time.NewTicker(time.Second)
	go func() {
		lastBytesPerSecond := uint64(0)
		lastTotaBytes := uint64(0)
		for {
			select {
			case <-ts.stoppedChannel:
				ts.reportingComplete <- nil
				return
			case <-ts.ticker.C:
				// The logging call may block, so we measure the current time
				// rather than use the time the from the ticker channel
				currentTime := time.Now()
				elapsed := currentTime.Sub(lastTime)
				lastTime = currentTime
				currentBytes := ts.currentPeriodBytes.Swap(0)
				bytesPerSecond := uint64(float64(currentBytes) / elapsed.Seconds())
				totalBytesSnapshot := ts.totalBytes.Load()
				if bytesPerSecond == 0 && lastBytesPerSecond == 0 && totalBytesSnapshot == lastTotaBytes {
					// No change in bytes per second or total bytes, and we logged 0 thoughput last time.
					continue
				}

				lastTotaBytes = totalBytesSnapshot
				lastBytesPerSecond = bytesPerSecond

				// For networking throughput in Mbps, we divide by 1000 * 1000 (not 1024 * 1024) because
				// networking is traditionally done in base 10 units (not base 2).
				partial := log.Ctx(ts.ctx).Info().Str("throughput", fmt.Sprintf("%sps", humanizeBytesAsBits(bytesPerSecond)))
				totalBuffers := ts.totalBuffers.Load()
				if totalBuffers > 0 {
					partial = partial.Str("totalBuffers", humanize.Comma(int64(totalBuffers)))
				}

				partial = partial.Str("totalCompletedData", removeSpaces(humanize.IBytes(totalBytesSnapshot)))

				partial.Msg("Transfer progress")
			}
		}
	}()

	log.Ctx(ts.ctx).Info().Msg("Transfer starting")
}

func (ts *TransferMetrics) Stop() {
	var elapsed time.Duration
	if ts.startTime != (time.Time{}) {
		elapsed = time.Since(ts.startTime)
		ts.stoppedChannel <- nil
		<-ts.reportingComplete
	}

	bytesPerSecond := uint64(float64(ts.totalBytes.Load()) / elapsed.Seconds())

	partial := log.Ctx(ts.ctx).Info().
		Str("elapsed", elapsed.Round(time.Second).String()).
		Str("avgThroughput", fmt.Sprintf("%sps", humanizeBytesAsBits(bytesPerSecond)))

	totalBuffers := ts.totalBuffers.Load()
	if totalBuffers > 0 {
		partial = partial.Uint64("totalBuffers", totalBuffers)
	}

	partial = partial.Str("totalCompletedData", removeSpaces(humanize.IBytes(ts.totalBytes.Load())))
	partial.Msg("Transfer complete")
}

func humanizeBytesAsBits(bytes uint64) string {
	s := humanize.Bytes(bytes * 8)
	return removeSpaces(strings.TrimSuffix(s, "B") + "b")
}

// "1 MB" -> "1MB" to that log field values are not quoted in the console
func removeSpaces(s string) string {
	return strings.Replace(s, " ", "", -1)
}

type DownloadProgressReader struct {
	Reader          io.ReadCloser
	TransferMetrics *TransferMetrics
}

func (pr *DownloadProgressReader) Read(p []byte) (int, error) {
	n, err := pr.Reader.Read(p)
	if n > 0 {
		pr.TransferMetrics.UpdateInFlight(uint64(n))
	}

	return n, err
}

func (pr *DownloadProgressReader) Close() error {
	return pr.Reader.Close()
}

type UploadProgressReader struct {
	Reader          *bytes.Reader
	TransferMetrics *TransferMetrics
}

func (pr *UploadProgressReader) Read(p []byte) (int, error) {
	n, err := pr.Reader.Read(p)
	if n > 0 {
		pr.TransferMetrics.UpdateInFlight(uint64(n))
	}

	return n, err
}

// Implement retryablehttp.LenReader
func (ts *UploadProgressReader) Len() int {
	return int(ts.Reader.Size())
}
