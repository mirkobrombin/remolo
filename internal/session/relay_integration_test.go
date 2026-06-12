package session

import (
	"bytes"
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/mirkobrombin/remolo/internal/protocol"
	"github.com/mirkobrombin/remolo/internal/relay"
	"github.com/mirkobrombin/remolo/internal/token"
)

// TestRelayPath exercises the full relay rung: the host registers with a relay,
// the client reaches it through the relay only (its direct candidates are
// removed), and a command round-trips. This proves the relay carries opaque
// ciphertext while authentication and channels work end to end.
func TestRelayPath(t *testing.T) {
	// Start a relay on a random local port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	relayAddr := ln.Addr().String()
	go relay.NewServer().Serve(ln)

	// Host registered with the relay.
	h, err := NewHost(HostOptions{Bind: "127.0.0.1:0", IncludeLoopback: true, TTL: time.Hour, Relay: relayAddr})
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); h.Close() }()
	go h.Serve(ctx)

	tok, err := token.Decode(h.Token())
	if err != nil {
		t.Fatal(err)
	}

	// Force the relay rung: strip direct candidates, keep a dummy rendezvous so
	// the token still validates, and connect with only the Relay option.
	relayOnly := *tok
	relayOnly.Candidates = nil
	relayOnly.Rendezvous = "http://127.0.0.1:1" // unreachable, lookup fails harmlessly

	cctx, ccancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer ccancel()

	cl, err := ConnectWith(cctx, &relayOnly, ConnectOptions{Relay: relayAddr}, nil)
	if err != nil {
		t.Fatalf("connect via relay: %v", err)
	}
	defer cl.Close("done")

	if !strings.HasPrefix(cl.Route(), "relay") {
		t.Fatalf("expected to connect via relay, got route %q", cl.Route())
	}

	stream, err := cl.OpenChannel(cctx, protocol.Open{Kind: protocol.KindExec, Command: []string{"/bin/echo", "relayed-ok"}})
	if err != nil {
		t.Fatalf("open channel: %v", err)
	}
	var out bytes.Buffer
	code, err := BridgeIO(stream, nil, &out, nil)
	if err != nil {
		t.Fatalf("bridge: %v", err)
	}
	if code != 0 || !strings.Contains(out.String(), "relayed-ok") {
		t.Fatalf("relayed exec failed: code=%d out=%q", code, out.String())
	}
}
