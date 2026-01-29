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

func createSshTunnelPoolClientFromContainer(ctx context.Context, tygerClient *client.TygerClient, container *Container, count int) (*retryablehttp.Client, *SshTunnelPool, error) {
	return CreateSshTunnelPoolClient(ctx, tygerClient, strings.Split(container.initialAccessUrl.Path, ":")[0], count)
}

func CreateSshTunnelPoolClient(ctx context.Context, tygerClient *client.TygerClient, socketPath string, count int) (*retryablehttp.Client, *SshTunnelPool, error) {
	controlPlaneSshParams, err := client.ParseSshUrl(tygerClient.RawControlPlaneUrl)
	if err != nil {
		return nil, nil, err
	}

	dpSshParams := *controlPlaneSshParams
	dpSshParams.SocketPath = socketPath

	tunnelPool := NewSshTunnelPool(ctx, dpSshParams, count)

	httpClient := client.CloneRetryableClient(tygerClient.DataPlaneClient.Client)
	httpClient.RequestLogHook = func(_ retryablehttp.Logger, r *http.Request, _ int) {
		r.URL = tunnelPool.GetUrl(r.URL)
	}

	return httpClient, tunnelPool, nil
}

type SshTunnelPool struct {
	socketPath     string
	sshParams      client.SshParams
	ctx            context.Context
	cancelCtx      context.CancelFunc
	mutex          sync.Mutex
	wg             sync.WaitGroup
	allTunnels     *list.List
	healthyTunnels []*sshTunnel
	index          int
}

func (tp *SshTunnelPool) Close() {
	log.Debug().Msg("Closing SSH tunnel pool")
	tp.cancelCtx()
	tp.mutex.Lock()
	for e := tp.allTunnels.Front(); e != nil; e = e.Next() {
		e.Value.(*sshTunnel).Close()
	}
	tp.mutex.Unlock()
	tp.wg.Wait()
	log.Debug().Msg("SSH tunnel pool closed")
}

func (tp *SshTunnelPool) GetUrl(input *url.URL) *url.URL {
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

func (tp *SshTunnelPool) watch(ctx context.Context, tunnel *sshTunnel) {
	active := true
	healthCheckEndpoint := fmt.Sprintf("http://%s/healthcheck", tunnel.Host)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthCheckEndpoint, nil)
	if err != nil {
		panic(err)
	}

	healthCheckTicker := time.NewTicker(1 * time.Second)
	defer healthCheckTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case exitErr := <-tunnel.exited:
			log.Warn().Str("host", tunnel.Host).Err(exitErr).Msg("SSH tunnel process exited")
			tp.removeTunnelFromHealthy(tunnel)
			tp.wg.Go(func() { tp.recreateTunnel(ctx) })
			return
		case <-healthCheckTicker.C:
			_, err := http.DefaultClient.Do(req)
			if err == nil {
				if !active {
					log.Ctx(ctx).Info().Str("host", tunnel.Host).Msg("SSH tunnel is active")
					tp.mutex.Lock()
					tp.healthyTunnels = append(tp.healthyTunnels, tunnel)
					tp.mutex.Unlock()
					active = true
				}
			} else {
				if errors.Is(err, ctx.Err()) {
					return
				}

				if active {
					active = false
					log.Warn().Str("host", tunnel.Host).Err(err).Msg("SSH tunnel is inactive")
					tp.removeTunnelFromHealthy(tunnel)

					// Close the dead tunnel and attempt to create a new one
					tunnel.Close()
					tp.wg.Go(func() { tp.recreateTunnel(ctx) })
					return
				}
			}
		}
	}
}

func (tp *SshTunnelPool) removeTunnelFromHealthy(tunnel *sshTunnel) {
	tp.mutex.Lock()
	defer tp.mutex.Unlock()
	for i, t := range tp.healthyTunnels {
		if t == tunnel {
			tp.healthyTunnels = append(tp.healthyTunnels[:i], tp.healthyTunnels[i+1:]...)
			break
		}
	}
}

func (tp *SshTunnelPool) recreateTunnel(ctx context.Context) {
	for retryCount := 0; ; retryCount++ {
		if ctx.Err() != nil {
			return
		}

		// Exponential backoff with jitter
		backoff := time.Duration(rand.IntnRange(200, 1500)*(1<<min(retryCount, 5))) * time.Millisecond
		if backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
		time.Sleep(backoff)

		tunnel, err := newSshTunnel(ctx, tp, tp.sshParams)
		if err != nil {
			var evt *zerolog.Event
			if retryCount > 5 {
				evt = log.Warn()
			} else {
				evt = log.Debug()
			}

			evt.Err(err).Int("retryCount", retryCount).Msg("Failed to recreate tunnel")
			continue
		}

		log.Info().Str("host", tunnel.Host).Msg("Successfully recreated SSH tunnel")
		tp.mutex.Lock()
		tp.healthyTunnels = append(tp.healthyTunnels, tunnel)
		tp.mutex.Unlock()

		tp.wg.Go(func() { tp.watch(ctx, tunnel) })
		return
	}
}

func NewSshTunnelPool(ctx context.Context, sshParams client.SshParams, count int) *SshTunnelPool {
	ctx, cancelCtx := context.WithCancel(ctx)

	pool := &SshTunnelPool{
		socketPath: sshParams.SocketPath,
		sshParams:  sshParams,
		ctx:        ctx,
		cancelCtx:  cancelCtx,
		mutex:      sync.Mutex{},
		allTunnels: list.New(),
	}

	for range count {
		pool.wg.Go(func() {
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

				pool.wg.Go(func() { pool.watch(ctx, tunnel) })
				return
			}
		})
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

func newSshTunnel(ctx context.Context, pool *SshTunnelPool, sshParams client.SshParams) (*sshTunnel, error) {
	port, err := GetFreePort()
	if err != nil {
		return nil, err
	}

	args := []string{
		"-nNT",
		"-o", "ControlMaster=no",
		"-o", "ControlPath=none",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"-o", "StrictHostKeyChecking=yes",
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
		err = fmt.Errorf("ssh tunnel closed: %w: %s", err, stdErr.String())
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
