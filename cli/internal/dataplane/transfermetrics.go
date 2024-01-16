// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package dataplane

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog/log"
)

type TransferMetrics struct {
	Context            context.Context
	Container          *Container
	totalBytes         uint64
	currentPeriodBytes uint64
	startTime          time.Time
	ticker             *time.Ticker
	stoppedChannel     chan any
	reportingComplete  chan any
}

func (ts *TransferMetrics) Update(byteCount uint64) {
	atomic.AddUint64(&ts.currentPeriodBytes, byteCount)
}

func (ts *TransferMetrics) Start() {
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
			case t := <-ts.ticker.C:
				elapsed := t.Sub(lastTime)
				lastTime = t
				currentBytes := atomic.SwapUint64(&ts.currentPeriodBytes, 0)
				if currentBytes > 0 {
					ts.totalBytes += currentBytes
					// For networking throughput in Mbps, we divide by 1000 * 1000 (not 1024 * 1024) because
					// networking is traditionally done in base 10 units (not base 2).
					currentMbps := float32(currentBytes*8) / (1000 * 1000) / float32(elapsed.Seconds())
					log.Ctx(ts.Context).Info().Float32("throughputMbps", currentMbps).Msg("Transfer progress")
				}
			}
		}
	}()

	log.Ctx(ts.Context).Info().Str("container", ts.Container.GetContainerName()).Msg("Transfer starting")
}

func (ts *TransferMetrics) Stop() {
	elapsed := time.Since(ts.startTime)
	ts.stoppedChannel <- nil
	<-ts.reportingComplete
	ts.totalBytes += atomic.SwapUint64(&ts.currentPeriodBytes, 0)
	log.Ctx(ts.Context).Info().
		Float32("elapsedSeconds", float32(elapsed.Seconds())).
		Float32("totalGiB", float32(ts.totalBytes)/(1024*1024*1024)).
		Msg("Transfer complete")
}
