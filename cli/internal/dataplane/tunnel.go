// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package dataplane

import (
	"bytes"
	"container/list"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/microsoft/tyger/cli/internal/client"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"k8s.io/apimachinery/pkg/util/rand"
)

// A pool of SSH tunnels for the dataplane.

func createSshTunnelPoolClient(ctx context.Context, tygerClient *client.TygerClient, container *Container, count int) (*retryablehttp.Client, *sshTunnelPool, error) {
	controlPlaneSshParams, err := client.ParseSshUrl(tygerClient.RawControlPlaneUrl)
	if err != nil {
		return nil, nil, err
	}

	dpSshParams := *controlPlaneSshParams

	dpSshParams.SocketPath = strings.Split(container.Path, ":")[0]

	tunnelPool := NewSshTunnelPool(ctx, dpSshParams, count)

	httpClient := client.CloneRetryableClient(tygerClient.DataPlaneClient.Client)
	httpClient.RequestLogHook = func(_ retryablehttp.Logger, r *http.Request, _ int) {
		r.URL = tunnelPool.GetUrl(r.URL)
	}

	return httpClient, tunnelPool, nil
}

type sshTunnelPool struct {
	socketPath     string
	ctx            context.Context
	cancelCtx      context.CancelFunc
	mutex          sync.Mutex
	allTunnels     *list.List
	healthyTunnels []*sshTunnel
	index          int
}

func (tp *sshTunnelPool) Close() {
	log.Debug().Msg("Closing SSH tunnel pool")
	tp.cancelCtx()
	tp.mutex.Lock()
	defer tp.mutex.Unlock()
	for e := tp.allTunnels.Front(); e != nil; e = e.Next() {
		e.Value.(*sshTunnel).Close()
	}
}

func (tp *sshTunnelPool) GetUrl(input *url.URL) *url.URL {
	tp.mutex.Lock()
	if len(tp.healthyTunnels) == 0 || tp.ctx.Err() != nil {
		tp.mutex.Unlock()

		if input.Scheme == "http" {
			outputUrl := *input
			outputUrl.Scheme = "http+unix"
			outputUrl.Host = ""
			outputUrl.Path = fmt.Sprintf("%s:%s", tp.socketPath, input.Path)
			return &outputUrl
		}
		return input
	}

	tp.index++
	tunnel := tp.healthyTunnels[tp.index%len(tp.healthyTunnels)]
	tp.mutex.Unlock()

	outputUrl := *input
	if input.Scheme == "http" {
		outputUrl := *input
		outputUrl.Host = tunnel.Host
		return &outputUrl
	}

	outputUrl.Scheme = "http"
	outputUrl.Host = tunnel.Host
	outputUrl.Path = input.Path[len(tp.socketPath)+1:]
	return &outputUrl
}

func (tp *sshTunnelPool) watch(ctx context.Context, tunnel *sshTunnel) {
	active := true
	healthCheckEndpoint := fmt.Sprintf("http://%s/healthcheck", tunnel.Host)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthCheckEndpoint, nil)
	if err != nil {
		panic(err)
	}

	for {
		if ctx.Err() != nil {
			return
		}

		_, err := http.DefaultClient.Do(req)
		if err == nil {
			if !active {
				log.Ctx(ctx).Info().Str("host", tunnel.Host).Msg("SSH tunnel is active")
				tp.mutex.Lock()
				tp.healthyTunnels = append(tp.healthyTunnels, tunnel)
				tp.mutex.Unlock()
			}
		} else {
			if errors.Is(err, ctx.Err()) {
				return
			}

			if active {
				active = false
				log.Warn().Str("host", tunnel.Host).Err(err).Msg("SSH tunnel is inactive")
				tp.mutex.Lock()
				for i, t := range tp.healthyTunnels {
					if t == tunnel {
						if len(tp.healthyTunnels) == 1 {
							tp.healthyTunnels = tp.healthyTunnels[:0]
						} else if i == len(tp.healthyTunnels)-1 {
							tp.healthyTunnels = tp.healthyTunnels[:i]
						} else if i == 0 {
							tp.healthyTunnels = tp.healthyTunnels[1:]
						} else {
							tp.healthyTunnels[i] = tp.healthyTunnels[len(tp.healthyTunnels)-1]
							tp.healthyTunnels = tp.healthyTunnels[:len(tp.healthyTunnels)-1]
						}
						break
					}
				}
				tp.mutex.Unlock()
			}
		}

		if active {
			time.Sleep(1 * time.Second)
		}
	}
}

func NewSshTunnelPool(ctx context.Context, sshParams client.SshParams, count int) *sshTunnelPool {
	ctx, cancelCtx := context.WithCancel(ctx)

	pool := &sshTunnelPool{
		socketPath: sshParams.SocketPath,
		ctx:        ctx,
		cancelCtx:  cancelCtx,
		mutex:      sync.Mutex{},
		allTunnels: list.New(),
	}

	for range count {
		go func() {
			for retryCount := 0; ; retryCount++ {
				if ctx.Err() != nil {
					return
				}

				tunnel, err := newSshTunnel(ctx, pool, sshParams)
				if err != nil {
					if retryCount > 10 {
						log.Error().Err(err).Msg("Giving up trying to create tunnel")
						return
					}

					var evt *zerolog.Event
					if retryCount > 5 {
						evt = log.Warn()
					} else {
						evt = log.Debug()
					}

					evt.Err(err).Msg("Failed to create tunnel")
					time.Sleep(time.Duration(rand.IntnRange(200, 1500) * int(time.Millisecond)))
					continue
				}

				pool.mutex.Lock()
				pool.healthyTunnels = append(pool.healthyTunnels, tunnel)
				pool.mutex.Unlock()

				go pool.watch(ctx, tunnel)
				return
			}
		}()
	}

	return pool
}

type sshTunnel struct {
	Port               int
	Host               string
	healthCheckRequest *http.Request
	command            *exec.Cmd
	exited             chan error
}

func (t *sshTunnel) Close() {
	if t.command.Process == nil {
		return
	}

	log.Debug().Int("port", t.Port).Msg("Closing SSH tunnel")
	t.command.Process.Kill()
	select {
	case <-t.exited:
	case <-time.After(500 * time.Millisecond):
	}
}

func (t *sshTunnel) healthCheck() error {
	_, err := http.DefaultClient.Do(t.healthCheckRequest)
	return err
}

func newSshTunnel(ctx context.Context, pool *sshTunnelPool, sshParams client.SshParams) (*sshTunnel, error) {
	port, err := GetFreePort()
	if err != nil {
		return nil, err
	}

	args := []string{
		"-nNT",
		"-o", "ControlMaster=no",
		"-o", "ControlPath=none",
		"-o", "ExitOnForwardFailure=yes",
		"-L", fmt.Sprintf("%d:%s", port, sshParams.SocketPath),
	}

	if sshParams.User != "" {
		args = append(args, "-l", sshParams.User)
	}
	if sshParams.Port != "" {
		args = append(args, "-p", sshParams.Port)
	}

	args = append(args, sshParams.Host)

	cmd := exec.Command("ssh", args...)
	log.Debug().Int("port", port).Msg("Creating SSH tunnel...")
	stdErr := &bytes.Buffer{}
	cmd.Stderr = stdErr

	healthCheckRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://localhost:%d/healthcheck", port), nil)
	if err != nil {
		return nil, err
	}

	tunnel := &sshTunnel{
		Port:               port,
		Host:               fmt.Sprintf("localhost:%d", port),
		healthCheckRequest: healthCheckRequest,
		command:            cmd,
		exited:             make(chan error),
	}

	pool.mutex.Lock()
	pool.allTunnels.PushBack(tunnel)
	pool.mutex.Unlock()

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	go func() {
		err := cmd.Wait()
		if ctx.Err() != nil {
			// ignore the error since we are cleaning up
			err = nil
		}
		log.Debug().Int("port", port).AnErr("error", err).Bytes("stderr", stdErr.Bytes()).Msg("SSH tunnel closed")
		tunnel.exited <- err
		close(tunnel.exited)
	}()

	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		err = tunnel.healthCheck()

		if err == nil {
			log.Debug().Int("port", port).Msg("SSH tunnel connection established.")
			return tunnel, nil
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case err := <-tunnel.exited:
			return nil, fmt.Errorf("tunnel exited: %w", err)
		case <-time.After(1 * time.Second):
		}
	}
}

func GetFreePort() (port int, err error) {
	var a *net.TCPAddr
	if a, err = net.ResolveTCPAddr("tcp", "localhost:0"); err == nil {
		var l *net.TCPListener
		if l, err = net.ListenTCP("tcp", a); err == nil {
			defer l.Close()
			return l.Addr().(*net.TCPAddr).Port, nil
		}
	}
	return
}
