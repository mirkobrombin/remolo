package session

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mirkobrombin/remolo/internal/protocol"
	"github.com/mirkobrombin/remolo/internal/token"
	"github.com/mirkobrombin/remolo/internal/transport"
)

// startHost spins up a loopback host and returns it with its decoded token.
func startHost(t *testing.T) (*Host, *token.Token, context.CancelFunc) {
	t.Helper()
	h, err := NewHost(HostOptions{Bind: "127.0.0.1:0", IncludeLoopback: true, TTL: time.Hour})
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go h.Serve(ctx)
	tok, err := token.Decode(h.Token())
	if err != nil {
		cancel()
		t.Fatalf("decode token: %v", err)
	}
	return h, tok, func() { cancel(); h.Close() }
}

func TestEndToEndPTY(t *testing.T) {
	_, tok, stop := startHost(t)
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cl, err := Connect(ctx, tok, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer cl.Close("done")

	caps := cl.Capabilities()
	if !caps.PTY {
		t.Fatal("expected host to advertise PTY capability")
	}

	stream, err := cl.OpenChannel(ctx, protocol.Open{
		Kind:    protocol.KindPTY,
		Cols:    80,
		Rows:    24,
		Command: []string{"/bin/sh", "-c", "echo SESSION-OK; exit 3"},
	})
	if err != nil {
		t.Fatalf("open pty channel: %v", err)
	}

	var out bytes.Buffer
	code, err := BridgeIO(stream, nil, &out, nil)
	if err != nil {
		t.Fatalf("bridge: %v", err)
	}
	if code != 3 {
		t.Errorf("exit code: want 3 got %d", code)
	}
	if !strings.Contains(out.String(), "SESSION-OK") {
		t.Errorf("missing shell output, got: %q", out.String())
	}
}

func TestEndToEndExec(t *testing.T) {
	_, tok, stop := startHost(t)
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cl, err := Connect(ctx, tok, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer cl.Close("done")

	stream, err := cl.OpenChannel(ctx, protocol.Open{
		Kind:    protocol.KindExec,
		Command: []string{"/bin/echo", "exec-works"},
	})
	if err != nil {
		t.Fatalf("open exec channel: %v", err)
	}
	var out bytes.Buffer
	code, err := BridgeIO(stream, nil, &out, nil)
	if err != nil {
		t.Fatalf("bridge: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code: want 0 got %d", code)
	}
	if !strings.Contains(out.String(), "exec-works") {
		t.Errorf("missing exec output, got: %q", out.String())
	}
}

func TestWrongTokenRejected(t *testing.T) {
	_, tok, stop := startHost(t)
	defer stop()

	// Tamper with the PSK: authentication must fail.
	bad := *tok
	bad.PSK = make([]byte, len(tok.PSK))
	copy(bad.PSK, tok.PSK)
	bad.PSK[0] ^= 0xff

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := Connect(ctx, &bad, nil); err == nil {
		t.Fatal("connect with wrong PSK should fail")
	}
}

func TestReportsDegradationResults(t *testing.T) {
	_, tok, stop := startHost(t)
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	var results []transport.Result
	cl, err := Connect(ctx, tok, func(r transport.Result) { results = append(results, r) })
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer cl.Close("done")
	if len(results) == 0 {
		t.Fatal("expected at least one attempt result for observability")
	}
}
