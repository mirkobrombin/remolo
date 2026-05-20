// Package rpc provides a small structured request/response control plane over a
// single remolo channel stream.
//
// It is the "gRPC-like" surface of remolo, but with zero external dependencies:
// requests and responses are JSON documents carried in FrameJSON frames. A
// Server registers named methods; a Client issues calls and matches responses
// by ID. The wire model is sequential per stream (one outstanding call at a
// time per stream), which keeps both ends trivial and avoids any multiplexing
// machinery beyond what the channel already gives us.
package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mirkobrombin/remolo/internal/protocol"
)

// Req is a single request frame.
type Req struct {
	ID     uint64          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Resp is the response to a Req with the matching ID.
type Resp struct {
	ID     uint64          `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// Handler implements a single RPC method. It receives the raw params and
// returns a value to be JSON-encoded as the result, or an error.
type Handler func(ctx context.Context, params json.RawMessage) (any, error)

// Server dispatches incoming requests to registered handlers.
type Server struct {
	mu       sync.RWMutex
	handlers map[string]Handler
}

// NewServer returns an empty Server with no methods registered.
func NewServer() *Server {
	return &Server{handlers: make(map[string]Handler)}
}

// Register binds a handler to a method name, replacing any prior registration.
func (s *Server) Register(method string, h Handler) {
	s.mu.Lock()
	s.handlers[method] = h
	s.mu.Unlock()
}

// Serve reads requests from the stream and writes responses until the stream is
// closed or a protocol error occurs. A clean EOF returns nil.
func (s *Server) Serve(stream io.ReadWriter) error {
	for {
		ft, payload, err := protocol.ReadFrame(stream)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("rpc: read frame: %w", err)
		}
		if ft != protocol.FrameJSON {
			// Ignore non-JSON frames rather than aborting the whole stream.
			continue
		}
		var req Req
		if err := json.Unmarshal(payload, &req); err != nil {
			resp := Resp{Error: fmt.Sprintf("rpc: bad request: %v", err)}
			if werr := protocol.WriteJSON(stream, resp); werr != nil {
				return werr
			}
			continue
		}

		resp := s.dispatch(context.Background(), req)
		if err := protocol.WriteJSON(stream, resp); err != nil {
			return fmt.Errorf("rpc: write response: %w", err)
		}
	}
}

// dispatch runs the handler for req and builds its Resp, never panicking out to
// the caller: a handler panic becomes an error response.
func (s *Server) dispatch(ctx context.Context, req Req) (resp Resp) {
	resp.ID = req.ID
	s.mu.RLock()
	h := s.handlers[req.Method]
	s.mu.RUnlock()
	if h == nil {
		resp.Error = fmt.Sprintf("rpc: unknown method %q", req.Method)
		return resp
	}

	defer func() {
		if r := recover(); r != nil {
			resp.Result = nil
			resp.Error = fmt.Sprintf("rpc: handler panic: %v", r)
		}
	}()

	result, err := h(ctx, req.Params)
	if err != nil {
		resp.Error = err.Error()
		return resp
	}
	if result != nil {
		b, merr := json.Marshal(result)
		if merr != nil {
			resp.Error = fmt.Sprintf("rpc: marshal result: %v", merr)
			return resp
		}
		resp.Result = b
	}
	return resp
}

// Client wraps a stream for sequential calls. It is safe for sequential use by a
// single goroutine; concurrent calls on one Client are not supported because the
// stream carries one request/response pair at a time.
type Client struct {
	stream io.ReadWriter
	nextID atomic.Uint64
}

// NewClient wraps a stream.
func NewClient(stream io.ReadWriter) *Client {
	return &Client{stream: stream}
}

// Call issues a method invocation, encoding params and decoding the result into
// result (which may be nil to discard the result).
func (c *Client) Call(method string, params any, result any) error {
	id := c.nextID.Add(1)
	return call(c.stream, id, method, params, result)
}

// Call is a one-shot convenience that issues a single request on the stream and
// decodes the matching response into result. It assigns ID 1; for multiple
// sequential calls on one stream prefer a Client.
func Call(stream io.ReadWriter, method string, params any, result any) error {
	return call(stream, 1, method, params, result)
}

func call(stream io.ReadWriter, id uint64, method string, params any, result any) error {
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("rpc: marshal params: %w", err)
		}
		raw = b
	}
	req := Req{ID: id, Method: method, Params: raw}
	if err := protocol.WriteJSON(stream, req); err != nil {
		return fmt.Errorf("rpc: send request: %w", err)
	}

	for {
		ft, payload, err := protocol.ReadFrame(stream)
		if err != nil {
			return fmt.Errorf("rpc: read response: %w", err)
		}
		if ft != protocol.FrameJSON {
			continue
		}
		var resp Resp
		if err := json.Unmarshal(payload, &resp); err != nil {
			return fmt.Errorf("rpc: decode response: %w", err)
		}
		if resp.ID != id {
			// Skip stale responses from an earlier call on this stream.
			continue
		}
		if resp.Error != "" {
			return fmt.Errorf("rpc: %s", resp.Error)
		}
		if result != nil && len(resp.Result) > 0 {
			if err := json.Unmarshal(resp.Result, result); err != nil {
				return fmt.Errorf("rpc: decode result: %w", err)
			}
		}
		return nil
	}
}

// --- Built-in handlers ----------------------------------------------------

// SysInfo is the result of the "sysinfo" method.
type SysInfo struct {
	OS       string  `json:"os"`
	Arch     string  `json:"arch"`
	Hostname string  `json:"hostname"`
	NumCPU   int     `json:"numcpu"`
	Uptime   float64 `json:"uptime"` // process uptime in seconds
}

// PathParams is the parameter shape for path-based methods (ls, stat).
type PathParams struct {
	Path string `json:"path"`
}

// DirEntry describes one entry returned by "ls".
type DirEntry struct {
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	IsDir bool   `json:"is_dir"`
	Mode  string `json:"mode"`
}

// StatResult is the result of "stat".
type StatResult struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	IsDir   bool   `json:"is_dir"`
	Mode    string `json:"mode"`
	ModTime int64  `json:"mod_time"` // unix seconds
}

// ExecParams is the parameter shape for "exec".
type ExecParams struct {
	Cmd []string `json:"cmd"`
}

// ExecResult is the result of "exec".
type ExecResult struct {
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
	Code   int    `json:"code"`
}

// startTime anchors the process-uptime reported by sysinfo.
var startTime = time.Now()

// DefaultServer returns a Server with the standard remolo control methods
// registered: sysinfo, ls, stat, and exec.
func DefaultServer() *Server {
	s := NewServer()
	s.Register("sysinfo", handleSysInfo)
	s.Register("ls", handleLs)
	s.Register("stat", handleStat)
	s.Register("exec", handleExec)
	return s
}

func handleSysInfo(_ context.Context, _ json.RawMessage) (any, error) {
	host, _ := os.Hostname()
	return SysInfo{
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		Hostname: host,
		NumCPU:   runtime.NumCPU(),
		Uptime:   time.Since(startTime).Seconds(),
	}, nil
}

func handleLs(_ context.Context, params json.RawMessage) (any, error) {
	var p PathParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	if p.Path == "" {
		p.Path = "."
	}
	entries, err := os.ReadDir(p.Path)
	if err != nil {
		return nil, fmt.Errorf("ls: %w", err)
	}
	out := make([]DirEntry, 0, len(entries))
	for _, e := range entries {
		var size int64
		mode := ""
		if info, ierr := e.Info(); ierr == nil {
			size = info.Size()
			mode = info.Mode().String()
		}
		out = append(out, DirEntry{
			Name:  e.Name(),
			Size:  size,
			IsDir: e.IsDir(),
			Mode:  mode,
		})
	}
	return out, nil
}

func handleStat(_ context.Context, params json.RawMessage) (any, error) {
	var p PathParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	if p.Path == "" {
		return nil, fmt.Errorf("stat: empty path")
	}
	info, err := os.Stat(p.Path)
	if err != nil {
		return nil, fmt.Errorf("stat: %w", err)
	}
	return StatResult{
		Name:    info.Name(),
		Size:    info.Size(),
		IsDir:   info.IsDir(),
		Mode:    info.Mode().String(),
		ModTime: info.ModTime().Unix(),
	}, nil
}

func handleExec(ctx context.Context, params json.RawMessage) (any, error) {
	var p ExecParams
	if err := decodeParams(params, &p); err != nil {
		return nil, err
	}
	if len(p.Cmd) == 0 {
		return nil, fmt.Errorf("exec: empty command")
	}
	cmd := exec.CommandContext(ctx, p.Cmd[0], p.Cmd[1:]...)
	var stdout, stderr writerBuf
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	code := 0
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			return nil, fmt.Errorf("exec: %w", err)
		}
	}
	return ExecResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
		Code:   code,
	}, nil
}

// decodeParams unmarshals raw params into v, tolerating empty params.
func decodeParams(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("rpc: decode params: %w", err)
	}
	return nil
}

// writerBuf accumulates an exec's output in memory. It is a two-line stand-in
// for bytes.Buffer, whose seek/read half this call site never uses.
type writerBuf struct{ b []byte }

func (w *writerBuf) Write(p []byte) (int, error) {
	w.b = append(w.b, p...)
	return len(p), nil
}

func (w *writerBuf) String() string { return string(w.b) }
