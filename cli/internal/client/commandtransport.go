// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package client

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"sync/atomic"

	"golang.org/x/sync/semaphore"
)

type CommandTransport struct {
	next    http.RoundTripper
	sem     *semaphore.Weighted
	command string
	args    []string
}

func MakeCommandTransport(concurrenyLimit int, command string, args ...string) MakeRoundTripper {
	return func(next http.RoundTripper) http.RoundTripper {
		return &CommandTransport{
			next:    next,
			sem:     semaphore.NewWeighted(int64(concurrenyLimit)),
			command: command,
			args:    args,
		}
	}
}

func (c *CommandTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL == nil || req.URL.Scheme != "http+unix" {
		return c.next.RoundTrip(req)
	}

	cmd := exec.CommandContext(req.Context(), c.command, c.args...)

	inPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("error creating stdin pipe: %w", err)
	}
	outPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("error creating stdout pipe: %w", err)
	}

	stdErr := &bytes.Buffer{}
	cmd.Stderr = stdErr

	if err := c.sem.Acquire(req.Context(), 1); err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		c.sem.Release(1)
		return nil, fmt.Errorf("error starting command: %w", err)
	}

	cleanedUp := atomic.Bool{}
	cleanup := func() {
		if cleanedUp.Swap(true) {
			return
		}

		cmd.Process.Kill()
		c.sem.Release(1)
		inPipe.Close()
		io.Copy(io.Discard, outPipe)
		cmd.Process.Wait()
		go cmd.Wait()
	}

	go req.WriteProxy(inPipe)

	resp, err := http.ReadResponse(bufio.NewReader(outPipe), req)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("error reading response over command: %w. stderr: %s", err, stdErr.String())
	}

	resp.Body = &cleanupOnCloseReader{
		ReadCloser: resp.Body,
		cleanup:    cleanup,
	}

	return resp, nil
}

type cleanupOnCloseReader struct {
	io.ReadCloser
	cleanup func()
}

func (m *cleanupOnCloseReader) Close() error {
	m.cleanup()
	return m.ReadCloser.Close()
}
