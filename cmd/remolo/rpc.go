package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/mirkobrombin/go-cli-builder/v2/pkg/cli"
	"github.com/mirkobrombin/remolo/internal/protocol"
	"github.com/mirkobrombin/remolo/internal/rest"
	"github.com/mirkobrombin/remolo/internal/rpc"
	"github.com/mirkobrombin/remolo/internal/session"
)

// InfoCmd queries the host for system information over the RPC channel.
type InfoCmd struct {
	Token string `arg:"" required:"true" help:"The session token"`
	JSON  bool   `cli:"json" help:"Output as JSON for scripting"`

	cli.Base
}

func (c *InfoCmd) Run() error {
	tok, err := decodeToken(c.Token)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs, err := openChannel(ctx, tok, protocol.Open{Kind: protocol.KindRPC}, session.ConnectOptions{}, c.Logger, false)
	if err != nil {
		return err
	}
	defer cs.cleanup()

	var info rpc.SysInfo
	if err := rpc.Call(cs.stream, "sysinfo", nil, &info); err != nil {
		return err
	}
	if c.JSON {
		b, _ := json.MarshalIndent(info, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	fmt.Printf("host:     %s\n", info.Hostname)
	fmt.Printf("os/arch:  %s/%s\n", info.OS, info.Arch)
	fmt.Printf("cpus:     %d\n", info.NumCPU)
	fmt.Printf("uptime:   %.0fs\n", info.Uptime)
	return nil
}

// RestCmd starts a local HTTP server that proxies a subset of operations to the
// host over RPC channels, for scripting and health checks (curl-friendly).
type RestCmd struct {
	Token string `arg:"" required:"true" help:"The session token"`
	Addr  string `cli:"addr" help:"Local listen address (default 127.0.0.1:8790)"`

	cli.Base
}

func (c *RestCmd) Run() error {
	tok, err := decodeToken(c.Token)
	if err != nil {
		return err
	}
	addr := c.Addr
	if addr == "" {
		addr = "127.0.0.1:8790"
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Establish the connection once and reuse it (via the mux daemon) for every
	// HTTP request, opening a fresh RPC channel per call.
	cs, err := openChannel(ctx, tok, protocol.Open{Kind: protocol.KindRPC}, session.ConnectOptions{}, c.Logger, true)
	if err != nil {
		return err
	}
	// The probe channel only forces connection (and mux daemon) setup; close it.
	// Subsequent per-request channels reuse the daemon cheaply.
	cs.stream.Close()

	dial := func() (io.ReadWriteCloser, error) {
		ch, e := openChannel(ctx, tok, protocol.Open{Kind: protocol.KindRPC}, session.ConnectOptions{}, c.Logger, false)
		if e != nil {
			return nil, e
		}
		return ch.stream, nil
	}

	mux := rest.NewMux(dial)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	fmt.Printf("remolo REST listening on http://%s (Ctrl-C to stop)\n", addr)
	fmt.Printf("  try: curl http://%s/sysinfo\n", addr)
	defer cs.cleanup()
	return srv.ListenAndServe()
}
