package main

import (
	"context"
	"fmt"
	"io"

	"github.com/mirkobrombin/go-cli-builder/v2/pkg/cli"
	"github.com/mirkobrombin/remolo/internal/protocol"
	"github.com/mirkobrombin/remolo/internal/session"
	"github.com/mirkobrombin/remolo/internal/webterm"
)

// WebtermCmd serves a browser-based terminal (xterm.js) driving a remote shell,
// for a no-install terminal from any browser on the local machine.
type WebtermCmd struct {
	Token  string `arg:"" required:"true" help:"The session token"`
	Addr   string `cli:"addr" help:"Local listen address (default 127.0.0.1:0)"`
	NoOpen bool   `cli:"no-open" help:"Do not auto-open the browser"`

	cli.Base
}

func (c *WebtermCmd) Run() error {
	tok, err := decodeToken(c.Token)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	open := func(cols, rows uint16) (io.ReadWriteCloser, error) {
		cs, err := openChannel(ctx, tok, protocol.Open{
			Kind: protocol.KindPTY, Cols: cols, Rows: rows, Term: "xterm-256color",
		}, session.ConnectOptions{}, c.Logger, false)
		if err != nil {
			return nil, err
		}
		return &cleanupRWC{Stream: cs.stream, cleanup: cs.cleanup}, nil
	}

	url, wait, err := webterm.Serve(ctx, webterm.Options{Addr: c.Addr}, open)
	if err != nil {
		return err
	}
	fmt.Printf("remolo: web terminal at %s (Ctrl-C to stop)\n", url)
	if !c.NoOpen {
		openBrowser(url)
	}
	return wait()
}

// cleanupRWC wraps a channel stream so Close also tears down the channel session.
type cleanupRWC struct {
	Stream  io.ReadWriteCloser
	cleanup func()
}

func (c *cleanupRWC) Read(p []byte) (int, error)  { return c.Stream.Read(p) }
func (c *cleanupRWC) Write(p []byte) (int, error) { return c.Stream.Write(p) }
func (c *cleanupRWC) Close() error {
	err := c.Stream.Close()
	if c.cleanup != nil {
		c.cleanup()
	}
	return err
}
