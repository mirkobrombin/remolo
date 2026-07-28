// Package webterm serves a local, browser-based terminal that drives a remote
// PTY over a remolo connection.
//
// The page (terminal.html, embedded) runs xterm.js. It opens a Server-Sent
// Events stream to /output to receive terminal output, POSTs keystrokes to
// /input, and POSTs window-size changes to /resize. On the Go side those routes
// speak remolo's frame protocol against a single PTY channel supplied by the
// integrator through an OpenPTY callback. SSE keeps the dependency surface to
// the standard library: no WebSocket library is required.
//
// One page load drives one PTY. The viewer is intended for a single local
// browser tab, so a single active PTY stream is tracked under a mutex; opening
// /output again replaces it.
package webterm

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"

	"github.com/mirkobrombin/remolo/internal/brand"
	"github.com/mirkobrombin/remolo/internal/protocol"
)

//go:embed terminal.html
var terminalHTML []byte

// Default terminal geometry used when the page does not supply cols/rows.
const (
	defaultCols uint16 = 80
	defaultRows uint16 = 24
)

// Options configures the local web terminal server.
type Options struct {
	// Addr is the local listen address. Empty means 127.0.0.1:0 (random port).
	Addr string
}

// OpenPTY opens a remolo PTY channel and returns it as a stream. The Open frame
// (Open{Kind: KindPTY, ...}) is expected to have already been written by the
// caller, so the returned stream is ready to carry data/resize/exit frames. The
// integrator supplies this; webterm only speaks frames on the result.
type OpenPTY func(cols, rows uint16) (io.ReadWriteCloser, error)

// server holds the single active PTY stream that /input and /resize write to.
type server struct {
	open OpenPTY

	mu  sync.Mutex
	pty io.ReadWriteCloser
}

// setPTY installs the active PTY stream, closing any previous one.
func (s *server) setPTY(p io.ReadWriteCloser) {
	s.mu.Lock()
	old := s.pty
	s.pty = p
	s.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
}

// currentPTY returns the active PTY stream, or nil if none is open.
func (s *server) currentPTY() io.ReadWriteCloser {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pty
}

// Serve binds a listener, builds the HTTP mux, and starts serving. It returns
// the page URL immediately along with a wait function that blocks until the
// server stops (because ctx is cancelled or Close is called). The caller drives
// the lifetime through ctx.
func Serve(ctx context.Context, opt Options, open OpenPTY) (url string, wait func() error, err error) {
	if open == nil {
		return "", nil, fmt.Errorf("webterm: OpenPTY callback is required")
	}
	addr := opt.Addr
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", nil, err
	}

	s := &server{open: open}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/favicon.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "max-age=86400")
		_, _ = w.Write(brand.Icon)
	})
	mux.HandleFunc("/output", s.handleOutput)
	mux.HandleFunc("/input", s.handleInput)
	mux.HandleFunc("/resize", s.handleResize)

	srv := &http.Server{Handler: mux}

	go func() {
		<-ctx.Done()
		_ = srv.Close()
		if p := s.currentPTY(); p != nil {
			_ = p.Close()
		}
	}()

	url = fmt.Sprintf("http://%s/", ln.Addr().String())

	done := make(chan error, 1)
	go func() {
		serveErr := srv.Serve(ln)
		if serveErr == http.ErrServerClosed {
			serveErr = nil
		}
		done <- serveErr
	}()

	wait = func() error { return <-done }
	return url, wait, nil
}

// handleIndex serves the embedded xterm.js page.
func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(terminalHTML)
}

// handleOutput opens a PTY and streams its FrameData output to the browser as
// Server-Sent Events. Each event carries one base64-encoded chunk of terminal
// bytes. The stream ends on FrameExit, a read error, or client disconnect.
func (s *server) handleOutput(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	cols := parseDim(r.URL.Query().Get("cols"), defaultCols)
	rows := parseDim(r.URL.Query().Get("rows"), defaultRows)

	pty, err := s.open(cols, rows)
	if err != nil {
		http.Error(w, "open pty: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.setPTY(pty)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Close the PTY if the browser disconnects, which unblocks ReadFrame.
	ctx := r.Context()
	go func() {
		<-ctx.Done()
		_ = pty.Close()
	}()

	enc := base64.StdEncoding
	for {
		ft, payload, readErr := protocol.ReadFrame(pty)
		if readErr != nil {
			return
		}
		switch ft {
		case protocol.FrameData:
			if len(payload) == 0 {
				continue
			}
			if _, err := io.WriteString(w, "data: "); err != nil {
				return
			}
			if _, err := io.WriteString(w, enc.EncodeToString(payload)); err != nil {
				return
			}
			if _, err := io.WriteString(w, "\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case protocol.FrameExit:
			return
		default:
			// Ignore other frame types (ping/pong/eof/etc) for the viewer.
		}
	}
}

// handleInput forwards request-body bytes to the active PTY as a FrameData
// frame (keystrokes).
func (s *server) handleInput(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	pty := s.currentPTY()
	if pty == nil {
		http.Error(w, "no active terminal", http.StatusConflict)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, protocol.MaxFramePayload))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(body) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := protocol.WriteFrame(pty, protocol.FrameData, body); err != nil {
		http.Error(w, "write: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleResize forwards a {cols,rows} JSON body to the active PTY as a
// FrameResize frame.
func (s *server) handleResize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	pty := s.currentPTY()
	if pty == nil {
		http.Error(w, "no active terminal", http.StatusConflict)
		return
	}
	var rs protocol.Resize
	if err := json.NewDecoder(io.LimitReader(r.Body, 256)).Decode(&rs); err != nil {
		http.Error(w, "decode resize: "+err.Error(), http.StatusBadRequest)
		return
	}
	payload, err := json.Marshal(rs)
	if err != nil {
		http.Error(w, "marshal resize: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := protocol.WriteFrame(pty, protocol.FrameResize, payload); err != nil {
		http.Error(w, "write: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// parseDim parses a terminal dimension query value, falling back to def on any
// problem and clamping to the uint16 range.
func parseDim(s string, def uint16) uint16 {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 || n > 65535 {
		return def
	}
	return uint16(n)
}
