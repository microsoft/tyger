package client

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os/exec"

	"golang.org/x/sync/semaphore"
)

type CommandTransport struct {
	sem     *semaphore.Weighted
	command string
	args    []string
}

func NewCommandTransport(concurrenyLimit int, command string, args ...string) *CommandTransport {
	return &CommandTransport{
		sem:     semaphore.NewWeighted(int64(concurrenyLimit)),
		command: command,
		args:    args,
	}
}

func (c *CommandTransport) RoundTrip(req *http.Request) (*http.Response, error) {

	cmd := exec.CommandContext(req.Context(), c.command, c.args...)
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			cmd.Process.Kill()
			cmd.Process.Wait()
		}

		return nil
	}

	inPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("error creating stdin pipe: %w", err)
	}
	outPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("error creating stdout pipe: %w", err)
	}

	errPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("error creating stderr pipe: %w", err)
	}

	stdErr := bytes.NewBuffer(nil)
	go func() {
		io.Copy(stdErr, errPipe)
	}()

	if err := c.sem.Acquire(req.Context(), 1); err != nil {
		return nil, err
	}

	defer c.sem.Release(1)

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("error starting command: %w", err)
	}

	go req.WriteProxy(inPipe)

	resp, err := http.ReadResponse(bufio.NewReader(outPipe), req)
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return nil, fmt.Errorf("error reading response over command: %w. stderr: %s", err, stdErr.String())
	}

	resp.Body = &waitOnCloseReader{ReadCloser: resp.Body, cmd: cmd}

	return resp, nil
}

type waitOnCloseReader struct {
	io.ReadCloser
	cmd *exec.Cmd
}

func (m *waitOnCloseReader) Close() error {
	m.cmd.Process.Kill()
	m.cmd.Wait()
	return m.ReadCloser.Close()
}

func MiddlewareFromTransport(transport http.RoundTripper) TransportMiddleware {
	return func(next http.RoundTripper) http.RoundTripper {
		return transport
	}
}
