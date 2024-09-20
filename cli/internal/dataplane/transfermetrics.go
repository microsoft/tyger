// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package dataplane

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/rs/zerolog/log"
)

type TransferMetrics struct {
	ctx                context.Context
	totalBuffers       atomic.Uint64
	totalBytes         uint64
	currentPeriodBytes atomic.Uint64
	startTime          time.Time
	ticker             *time.Ticker
	stoppedChannel     chan any
	reportingComplete  chan any
}

func NewTransferMetrics(ctx context.Context) *TransferMetrics {
	return &TransferMetrics{
		ctx: ctx,
	}
}

func (ts *TransferMetrics) Update(byteCount uint64, completedBuffers uint64) {
	newPeriodBytes := ts.currentPeriodBytes.Add(byteCount)
	newCompletedBuffers := ts.totalBuffers.Add(completedBuffers)

	if (byteCount > 0 && newPeriodBytes == byteCount || completedBuffers > 0 && newCompletedBuffers == completedBuffers) && ts.startTime == (time.Time{}) {
		ts.start()
	}
}

func (ts *TransferMetrics) start() {
	ts.stoppedChannel = make(chan any)
	ts.reportingComplete = make(chan any)
	ts.ticker = time.NewTicker(2 * time.Second)
	lastTime := time.Now()
	ts.startTime = lastTime
	go func() {
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
				ts.totalBytes += currentBytes
				bytesPerSecond := uint64(float64(currentBytes) / elapsed.Seconds())
				// For networking throughput in Mbps, we divide by 1000 * 1000 (not 1024 * 1024) because
				// networking is traditionally done in base 10 units (not base 2).
				partial := log.Ctx(ts.ctx).Info().Str("throughput", fmt.Sprintf("%sps", humanizeBytesAsBits(bytesPerSecond)))
				totalBuffers := ts.totalBuffers.Load()
				if totalBuffers > 0 {
					partial = partial.Str("totalBuffers", humanize.Comma(int64(totalBuffers)))
				}

				partial = partial.Str("totalData", removeSpaces(humanize.IBytes(ts.totalBytes)))

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
		ts.totalBytes += ts.currentPeriodBytes.Load()
	}

	bytesPerSecond := uint64(float64(ts.totalBytes) / elapsed.Seconds())

	partial := log.Ctx(ts.ctx).Info().
		Str("elapsed", elapsed.Round(time.Second).String()).
		Str("avgThroughput", fmt.Sprintf("%sps", humanizeBytesAsBits(bytesPerSecond)))

	totalBuffers := ts.totalBuffers.Load()
	if totalBuffers > 0 {
		partial = partial.Uint64("totalBuffers", totalBuffers)
	}

	partial = partial.Str("totalData", removeSpaces(humanize.IBytes(ts.totalBytes)))
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
