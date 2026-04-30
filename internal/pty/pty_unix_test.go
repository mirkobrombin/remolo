//go:build linux || darwin || freebsd || netbsd || openbsd

package pty

import (
	"bytes"
	"io"
	"testing"
	"time"
)

func TestStartEchoAndExitCode(t *testing.T) {
	p, err := Start(Config{Command: []string{"/bin/sh", "-c", "echo hello-remolo; exit 7"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}

	// Drain output until the PTY closes.
	var buf bytes.Buffer
	doneRead := make(chan struct{})
	go func() {
		io.Copy(&buf, p)
		close(doneRead)
	}()

	code, err := p.Wait()
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if code != 7 {
		t.Fatalf("exit code: want 7 got %d", code)
	}
	p.Close()

	select {
	case <-doneRead:
	case <-time.After(2 * time.Second):
	}
	if !bytes.Contains(buf.Bytes(), []byte("hello-remolo")) {
		t.Fatalf("output missing echo, got: %q", buf.String())
	}
}

func TestInteractiveWrite(t *testing.T) {
	p, err := Start(Config{Command: []string{"/bin/sh"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	out := make(chan []byte, 1)
	go func() {
		b := make([]byte, 4096)
		var acc []byte
		deadline := time.After(2 * time.Second)
		for {
			select {
			case <-deadline:
				out <- acc
				return
			default:
			}
			n, err := p.Read(b)
			if n > 0 {
				acc = append(acc, b[:n]...)
				if bytes.Contains(acc, []byte("MARKER-OK")) {
					out <- acc
					return
				}
			}
			if err != nil {
				out <- acc
				return
			}
		}
	}()

	if _, err := io.WriteString(p, "echo MARKER-OK\n"); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := <-out
	if !bytes.Contains(got, []byte("MARKER-OK")) {
		t.Fatalf("did not observe command output, got: %q", got)
	}

	if err := p.Resize(120, 40); err != nil {
		t.Fatalf("resize: %v", err)
	}
}
