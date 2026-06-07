package session

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/mirkobrombin/remolo/internal/protocol"
	"github.com/mirkobrombin/remolo/internal/transport"
)

// readUntil reads frames from the stream until the marker appears in FrameData
// output or the deadline passes.
func readUntil(t *testing.T, stream transport.Stream, marker []byte, d time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	var acc []byte
	for time.Now().Before(deadline) {
		stream.SetDeadline(time.Now().Add(500 * time.Millisecond))
		ft, payload, err := protocol.ReadFrame(stream)
		if err != nil {
			continue
		}
		if ft == protocol.FrameData {
			acc = append(acc, payload...)
			if bytes.Contains(acc, marker) {
				return true
			}
		}
	}
	return false
}

// TestRoamingDetachReattach proves the mosh-grade core: a PTY with a ResumeID
// survives a dropped stream (the shell keeps running, detached) and a second
// channel with the same ResumeID reattaches, replaying earlier output and
// continuing the same shell.
func TestRoamingDetachReattach(t *testing.T) {
	_, tok, stop := startHost(t)
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cl, err := Connect(ctx, tok, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer cl.Close("done")

	open := protocol.Open{Kind: protocol.KindPTY, Cols: 80, Rows: 24, Command: []string{"/bin/sh"}, ResumeID: "roam-test-1"}

	// First attachment: run a command, observe its output.
	s1, err := cl.OpenChannel(ctx, open)
	if err != nil {
		t.Fatalf("open 1: %v", err)
	}
	protocol.WriteFrame(s1, protocol.FrameData, []byte("echo MARKER-ONE\n"))
	if !readUntil(t, s1, []byte("MARKER-ONE"), 6*time.Second) {
		t.Fatal("did not see MARKER-ONE on first attachment")
	}

	// Simulate a network drop: close the stream. The host should DETACH (keep
	// the shell alive), not kill it.
	s1.Close()
	time.Sleep(500 * time.Millisecond)

	// Reattach with the same ResumeID: the host replays the ring (MARKER-ONE)
	// and the same shell continues (a new command works).
	s2, err := cl.OpenChannel(ctx, open)
	if err != nil {
		t.Fatalf("open 2 (reattach): %v", err)
	}
	if !readUntil(t, s2, []byte("MARKER-ONE"), 6*time.Second) {
		t.Fatal("reattach did not replay earlier output (MARKER-ONE)")
	}
	protocol.WriteFrame(s2, protocol.FrameData, []byte("echo MARKER-TWO\n"))
	if !readUntil(t, s2, []byte("MARKER-TWO"), 6*time.Second) {
		t.Fatal("shell did not survive the reattach (no MARKER-TWO)")
	}
}
