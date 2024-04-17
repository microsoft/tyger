// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package proxy

import (
	"io"
	"net/http"

	pool "github.com/libp2p/go-buffer-pool"
)

func CopyResponse(w http.ResponseWriter, resp *http.Response) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		// The ResponseWriter doesn't support flushing, fallback to simple copy
		_, err := io.Copy(w, resp.Body)
		return err
	}

	// Copy with flushing whenever there is data so that a trickle of data does not get buffered
	// and result in high latency

	buf := pool.Get(32 * 1024)
	defer func() {
		pool.Put(buf)
	}()

	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			flusher.Flush()
		}
		if err != nil {
			if err != io.EOF {
				return err
			}
			return nil
		}
	}
}
