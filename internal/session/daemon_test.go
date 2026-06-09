package session

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mirkobrombin/remolo/internal/protocol"
)

func TestMuxDaemonReuse(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())

	_, tok, stop := startHost(t)
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Primary: establish the connection and own the daemon.
	primary, err := Connect(ctx, tok, nil)
	if err != nil {
		t.Fatalf("primary connect: %v", err)
	}
	defer primary.Close("done")

	d, err := StartDaemon(primary, tok.SessionID, nil)
	if err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	defer d.Close()

	// A live daemon must now be discoverable.
	if !HasDaemon(tok.SessionID) {
		t.Fatal("expected daemon to be discoverable")
	}
	sessions, err := ListSessions()
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}

	// Secondary: open a channel through the daemon, reusing the connection
	// (no second handshake or dial).
	ch, ok, err := DialDaemon(tok.SessionID, protocol.Open{
		Kind:    protocol.KindExec,
		Command: []string{"/bin/echo", "reused-connection"},
	})
	if err != nil || !ok {
		t.Fatalf("dial daemon: ok=%v err=%v", ok, err)
	}
	var out bytes.Buffer
	code, err := BridgeIO(ch, nil, &out, nil)
	if err != nil {
		t.Fatalf("bridge via daemon: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code via daemon: want 0 got %d", code)
	}
	if !strings.Contains(out.String(), "reused-connection") {
		t.Errorf("missing output via daemon, got: %q", out.String())
	}
}

func TestNoDaemonReportsAbsent(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	fakeID := make([]byte, 16)
	if HasDaemon(fakeID) {
		t.Fatal("did not expect a daemon for a random session id")
	}
	_, ok, err := DialDaemon(fakeID, protocol.Open{Kind: protocol.KindExec, Command: []string{"/bin/true"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("did not expect to reach a daemon")
	}
	_ = time.Second
}
