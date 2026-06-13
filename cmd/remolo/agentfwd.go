package main

import (
	"context"
	"fmt"
	"net"
	"os"

	"github.com/mirkobrombin/remolo/internal/protocol"
	"github.com/mirkobrombin/remolo/internal/session"
	"github.com/mirkobrombin/remolo/internal/token"
	"github.com/mirkobrombin/remolo/internal/transport"
	"github.com/mirkobrombin/remolo/internal/tunnel"
)

// runWithAgent connects with SSH agent forwarding: it enables forwarding on the
// host, then services host-initiated agent channels by splicing them to the
// local SSH agent, so the remote shell can use the local keys (ssh -A).
func (c *ConnectCmd) runWithAgent(ctx context.Context, tok *token.Token, open protocol.Open) error {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return fmt.Errorf("no local SSH agent (SSH_AUTH_SOCK is unset)")
	}

	cl, err := session.ConnectWith(ctx, tok, c.ladderOptions(), nil)
	if err != nil {
		return err
	}
	defer cl.Close("session ended")

	if err := cl.RequestAgentForward(); err != nil {
		return fmt.Errorf("agent forwarding: %w", err)
	}
	fmt.Printf("remolo: connected via %s (SSH agent forwarded)\n", cl.Route())

	// Service host-initiated agent channels.
	go func() {
		for {
			o, stream, err := cl.AcceptChannel(ctx)
			if err != nil {
				return
			}
			if o.Kind != protocol.KindAgent {
				stream.Close()
				continue
			}
			go func(s transport.Stream) {
				a, derr := net.Dial("unix", sock)
				if derr != nil {
					s.Close()
					return
				}
				tunnel.Splice(a, s)
			}(stream)
		}
	}()

	stream, err := cl.OpenChannel(ctx, open)
	if err != nil {
		return err
	}
	code, err := runInteractiveShell(stream)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nremolo: session ended (%v)\n", err)
	}
	if code != 0 {
		os.Exit(code)
	}
	return nil
}
