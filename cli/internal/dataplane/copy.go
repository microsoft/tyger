// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package dataplane

import (
	"errors"
	"io"
	"sync"
)

// A copy implementation that reads and writes concurrently
// and also makes an effort to write 64KB at a time, to increase throughput.
func copyToPipe(dst io.Writer, src io.Reader) error {
	type buffer struct {
		mutex     sync.Mutex
		data      []byte
		n         int
		consuming bool
	}

	const bufferSize = 64 * 1024
	const maxBuffers = 5
	readyBuffers := make(chan *buffer, maxBuffers)
	emptyBuffers := make(chan *buffer, maxBuffers)
	readErr := make(chan error, 1)
	abandon := make(chan any)
	abandonedErr := errors.New("abandoned copy")

	go func() {
		remainingAllocs := maxBuffers
		getBuffer := func() *buffer {
			var buf *buffer
			if remainingAllocs > 0 {
				select {
				case buf = <-emptyBuffers:
				case <-abandon:
					readErr <- abandonedErr
					return nil
				default:
					buf = &buffer{
						mutex: sync.Mutex{},
						data:  make([]byte, bufferSize),
					}

					remainingAllocs--
				}
			} else {
				buf = <-emptyBuffers
			}

			buf.consuming = false
			buf.n = 0
			return buf
		}

		for {
			buf := getBuffer()
			if buf == nil {
				readErr <- abandonedErr
				close(readyBuffers)
				return
			}

			totalRead, err := src.Read(buf.data)

			if totalRead > 0 {
				buf.n = totalRead
				readyBuffers <- buf
			}
			if err != nil {
				close(readyBuffers)
				readErr <- err
				return
			}

			for totalRead <= bufferSize/2 {
				buf.mutex.Lock()
				if buf.consuming {
					buf.mutex.Unlock()
					break
				}
				buf.mutex.Unlock()

				select {
				case <-abandon:
					readErr <- abandonedErr
					close(readyBuffers)
					return
				default:
				}

				remaining := buf.data[totalRead:]
				nThisCycle, err := src.Read(remaining)
				if nThisCycle > 0 {
					buf.mutex.Lock()
					if !buf.consuming {
						buf.n += nThisCycle
						buf.mutex.Unlock()
						totalRead += nThisCycle
					} else {
						buf.mutex.Unlock()
						buf = getBuffer()
						if buf == nil {
							readErr <- abandonedErr
							close(readyBuffers)
							return
						}

						copy(buf.data, remaining[:nThisCycle])
						buf.n = nThisCycle
						totalRead = nThisCycle
						readyBuffers <- buf
					}
				}

				if err != nil {
					close(readyBuffers)
					readErr <- err
					return
				}
			}

		}
	}()

	for buf := range readyBuffers {
		buf.mutex.Lock()
		buf.consuming = true
		buf.mutex.Unlock()

		_, err := dst.Write(buf.data[:buf.n])
		if err != nil {
			close(abandon)
			return err
		}

		emptyBuffers <- buf
	}

	if err := <-readErr; err != io.EOF {
		return err
	}

	return nil
}
