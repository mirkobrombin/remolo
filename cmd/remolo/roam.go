package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/mirkobrombin/remolo/internal/protocol"
	"github.com/mirkobrombin/remolo/internal/session"
	"github.com/mirkobrombin/remolo/internal/token"
	"github.com/mirkobrombin/remolo/internal/transport"
	"golang.org/x/term"
)

// reconnectWindow bounds how long roaming keeps trying to re-reach the host
// after the connection drops before giving up.
const reconnectWindow = 90 * time.Second

// roamStreams holds the current live stream, swapped on each reconnect, so the
// single persistent stdin/resize readers always target the active connection.
type roamStreams struct {
	mu  sync.Mutex
	cur transport.Stream
}

func (r *roamStreams) set(s transport.Stream) {
	r.mu.Lock()
	r.cur = s
	r.mu.Unlock()
}

func (r *roamStreams) writeFrame(ft protocol.FrameType, b []byte) {
	r.mu.Lock()
	s := r.cur
	r.mu.Unlock()
	if s != nil {
		_ = protocol.WriteFrame(s, ft, b)
	}
}

// runRoaming runs a mosh-grade resumable shell: the host keeps the shell alive
// across drops, and the client reconnects and reattaches by ResumeID, replaying
// recent output. One stdin/resize reader spans all reconnects.
func (c *ConnectCmd) runRoaming(ctx context.Context, tok *token.Token, open protocol.Open) error {
	idb := make([]byte, 16)
	rand.Read(idb)
	open.ResumeID = hex.EncodeToString(idb)

	fd := int(os.Stdin.Fd())
	var restore func()
	if term.IsTerminal(fd) {
		if old, err := term.MakeRaw(fd); err == nil {
			restore = func() { term.Restore(fd, old) }
		}
	}
	if restore != nil {
		defer restore()
	}

	roam := &roamStreams{}

	// One persistent stdin reader for the whole roaming session.
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				roam.writeFrame(protocol.FrameData, append([]byte(nil), buf[:n]...))
			}
			if err != nil {
				return
			}
		}
	}()

	// Forward terminal resizes to whatever stream is current.
	resize := make(chan protocol.Resize, 1)
	stopResize := installResizeHandler(fd, resize)
	defer stopResize()
	go func() {
		for r := range resize {
			b, _ := json.Marshal(r)
			roam.writeFrame(protocol.FrameResize, b)
		}
	}()

	opts := c.ladderOptions()
	lostSince := time.Time{}
	for {
		cl, err := session.ConnectWith(ctx, tok, opts, nil)
		if err != nil {
			if lostSince.IsZero() {
				lostSince = time.Now()
			}
			if time.Since(lostSince) > reconnectWindow {
				return fmt.Errorf("roaming: host unreachable for %s", reconnectWindow)
			}
			time.Sleep(time.Second)
			continue
		}
		lostSince = time.Time{}

		stream, err := cl.OpenChannel(ctx, open)
		if err != nil {
			cl.Close("roam reopen")
			time.Sleep(time.Second)
			continue
		}
		roam.set(stream)

		code, lost := pumpOutput(stream)
		roam.set(nil)
		cl.Close("roam cycle")
		if !lost {
			if restore != nil {
				restore()
			}
			if code != 0 {
				os.Exit(code)
			}
			return nil
		}
		fmt.Fprint(os.Stderr, "\r\n[remolo: connection lost, reconnecting...]\r\n")
	}
}

// pumpOutput copies the stream's output to the terminal until the shell exits
// (clean, lost=false) or the connection drops (lost=true).
func pumpOutput(stream transport.Stream) (code int, lost bool) {
	for {
		ft, payload, err := protocol.ReadFrame(stream)
		if err != nil {
			return -1, true
		}
		switch ft {
		case protocol.FrameData:
			os.Stdout.Write(payload)
		case protocol.FrameStderr:
			os.Stderr.Write(payload)
		case protocol.FrameExit:
			var ex protocol.Exit
			_ = json.Unmarshal(payload, &ex)
			return ex.Code, false
		}
	}
}
