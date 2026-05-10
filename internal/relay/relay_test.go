package relay

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/mirkobrombin/remolo/internal/transport"
	"github.com/mirkobrombin/remolo/internal/transport/tcpmux"
)

// startRelay spins up a relay on a random local port and returns its address
// plus a stop function.
func startRelay(t *testing.T) (addr string, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := NewServer()
	go srv.Serve(ln)
	return ln.Addr().String(), func() { ln.Close() }
}

// hostEcho runs the host side: connect to the relay, wrap with a yamux server
// session and echo every inbound stream. It signals readiness once the
// relayed conn is established and the server session is up.
func hostEcho(t *testing.T, relayAddr, key string, ready chan<- error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	raw, err := ListenRelay(ctx, relayAddr, key)
	if err != nil {
		ready <- err
		return
	}
	conn, err := tcpmux.NewServerConn(raw)
	if err != nil {
		ready <- err
		return
	}
	ready <- nil
	for {
		s, err := conn.AcceptStream(context.Background())
		if err != nil {
			return
		}
		go func(s transport.Stream) {
			defer s.Close()
			io.Copy(s, s)
		}(s)
	}
}

func TestRelayRoundTrip(t *testing.T) {
	relayAddr, stop := startRelay(t)
	defer stop()

	const key = "deadbeefcafe0001"
	ready := make(chan error, 1)
	go hostEcho(t, relayAddr, key, ready)

	// Wait for the host to register and have its server session ready before
	// the client dials, so the relay has a waiting host to pair with.
	if err := <-ready; err != nil {
		t.Fatalf("host setup: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := DialRelay(ctx, relayAddr, key, nil)
	if err != nil {
		t.Fatalf("dial relay: %v", err)
	}
	defer conn.Close("done")

	s, err := conn.OpenStream(ctx)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer s.Close()

	msg := []byte("relayed bytes round-trip")
	if _, err := s.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(s, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(msg) {
		t.Fatalf("got %q want %q", got, msg)
	}
}

func TestRelayWrongKeyNoMatch(t *testing.T) {
	relayAddr, stop := startRelay(t)
	defer stop()

	ready := make(chan error, 1)
	go hostEcho(t, relayAddr, "aaaa", ready)
	if err := <-ready; err != nil {
		t.Fatalf("host setup: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// A client with a different key must not be paired with the host. The
	// relay drops the client connection, so the yamux session has no live peer
	// and opening a stream over it fails.
	conn, err := DialRelay(ctx, relayAddr, "bbbb", nil)
	if err != nil {
		return // failed already at dial/handshake, which is also acceptable
	}
	defer conn.Close("done")
	s, err := conn.OpenStream(ctx)
	if err != nil {
		return // no peer, opening failed: acceptable
	}
	// With no matching host there is no echo peer, so a written byte is never
	// answered: a bounded read must fail rather than return echoed data.
	s.SetDeadline(time.Now().Add(time.Second))
	if _, err := s.Write([]byte("ping")); err != nil {
		return
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(s, buf); err == nil {
		t.Fatalf("expected no echo for unmatched relay key, got %q", buf)
	}
}
