package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mirkobrombin/go-cli-builder/v2/pkg/cli"
	"github.com/mirkobrombin/remolo/internal/protocol"
	"github.com/mirkobrombin/remolo/internal/session"
	"github.com/mirkobrombin/remolo/internal/tunnel"
)

// ForwardCmd sets up port forwarding over the remolo connection, SSH-style.
//
//	-L localport:host:port   forward a local port to a target reachable by the host
//	-D socksport             dynamic SOCKS5 proxy (browser-friendly)
type ForwardCmd struct {
	Token string `arg:"" required:"true" help:"The session token"`
	Local string `cli:"local,L" help:"Local forward: [bind:]lport:host:rport"`
	Socks string `cli:"socks,D" help:"Dynamic SOCKS5 proxy on [bind:]port"`

	cli.Base
}

func (c *ForwardCmd) Run() error {
	if c.Local == "" && c.Socks == "" {
		return fmt.Errorf("specify -L localport:host:port and/or -D socksport")
	}
	tok, err := decodeToken(c.Token)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// A dedicated connection for the lifetime of the forwarder; each forwarded
	// TCP connection becomes its own channel on it.
	cl, err := session.ConnectWith(ctx, tok, session.ConnectOptions{}, nil)
	if err != nil {
		return err
	}
	defer cl.Close("forward ended")
	fmt.Printf("remolo: forwarding via %s (Ctrl-C to stop)\n", cl.Route())

	open := func(ctx context.Context, target string) (io.ReadWriteCloser, error) {
		return cl.OpenChannel(ctx, protocol.Open{Kind: protocol.KindForward, Target: target})
	}

	errc := make(chan error, 2)
	if c.Local != "" {
		listen, target, perr := parseLocalForward(c.Local)
		if perr != nil {
			return perr
		}
		fmt.Printf("remolo: -L %s -> %s (via host)\n", listen, target)
		go func() { errc <- tunnel.LocalForward(ctx, listen, target, open) }()
	}
	if c.Socks != "" {
		listen := withDefaultBind(c.Socks)
		fmt.Printf("remolo: -D SOCKS5 on %s\n", listen)
		go func() { errc <- tunnel.SocksForward(ctx, listen, open) }()
	}

	err = <-errc
	if ctx.Err() != nil {
		return nil
	}
	return err
}

// parseLocalForward parses [bind:]lport:host:rport into a listen addr and target.
func parseLocalForward(spec string) (listen, target string, err error) {
	parts := strings.Split(spec, ":")
	switch len(parts) {
	case 3: // lport:host:rport
		return "127.0.0.1:" + parts[0], parts[1] + ":" + parts[2], nil
	case 4: // bind:lport:host:rport
		return parts[0] + ":" + parts[1], parts[2] + ":" + parts[3], nil
	default:
		return "", "", fmt.Errorf("invalid -L spec %q (want [bind:]lport:host:rport)", spec)
	}
}

func withDefaultBind(spec string) string {
	if strings.Contains(spec, ":") {
		return spec
	}
	return "127.0.0.1:" + spec
}
