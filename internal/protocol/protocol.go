// Package protocol defines remolo's on-the-wire framing and the messages
// exchanged over logical channels.
//
// Every logical channel is a single transport stream. The first frame on a
// stream is always an Open frame (JSON) describing the channel's kind and
// parameters; everything after that is a sequence of typed frames. Typed
// framing lets a channel carry bulk data alongside out-of-band control signals
// (terminal resize, process exit, keepalive) without a separate coordination
// channel.
//
// Frame layout:
//
//	[1 byte type][4 byte big-endian length][payload ...]
package protocol

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

// Kind identifies the purpose of a logical channel.
type Kind string

const (
	KindControl Kind = "control" // capability negotiation, keepalive
	KindPTY     Kind = "pty"     // interactive shell
	KindExec    Kind = "exec"    // one-shot command
	KindDesktop Kind = "desktop" // screen + input (negotiated, may degrade)
	KindFile    Kind = "file"    // file transfer
	KindRPC     Kind = "rpc"     // structured request/response (sysinfo, fileops)
	KindForward Kind = "forward" // TCP port forwarding (a tunnelled connection)
	KindSync    Kind = "sync"    // incremental directory sync (rsync-like)
	KindAgent   Kind = "agent"   // host-initiated: SSH agent forwarding back to the client
)

// ControlMsg is a JSON message exchanged on the control channel after the
// handshake (e.g. to request SSH agent forwarding).
type ControlMsg struct {
	Type string `json:"type"`
}

// FrameType tags a frame's payload.
type FrameType byte

const (
	FrameData   FrameType = iota + 1 // raw bytes (stdin/stdout)
	FrameResize                      // JSON Resize
	FrameSignal                      // JSON Signal
	FrameExit                        // JSON Exit
	FrameJSON                        // JSON control message (Open, Capabilities, ...)
	FramePing                        // keepalive request (empty)
	FramePong                        // keepalive reply (empty)
	FrameEOF                         // sender finished writing data (empty)
	FrameStderr                      // raw bytes (stderr, kept separate from stdout)
)

// MaxFramePayload caps a single frame to keep a (post-authentication) peer from
// forcing an unbounded allocation. Bulk data (files, shell output) is chunked
// far below this; full-screen desktop keyframes are the large case, so the cap
// is generous but still bounded.
const MaxFramePayload = 16 << 20 // 16 MiB

// Open is the first frame on every channel.
type Open struct {
	Kind     Kind              `json:"kind"`
	Cols     uint16            `json:"cols,omitempty"`
	Rows     uint16            `json:"rows,omitempty"`
	Command  []string          `json:"command,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Term     string            `json:"term,omitempty"`
	Dir      string            `json:"dir,omitempty"`    // remote working directory
	Target   string            `json:"target,omitempty"` // forward target host:port
	ResumeID string            `json:"resume,omitempty"` // detachable-session id (roaming)
}

// Resize updates a PTY's window size.
type Resize struct {
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

// Signal forwards a signal name (e.g. "INT") to the remote process.
type Signal struct {
	Name string `json:"name"`
}

// Exit reports a remote process's exit status.
type Exit struct {
	Code int    `json:"code"`
	Err  string `json:"err,omitempty"`
}

// Capabilities is exchanged on the control channel so peers agree on what is
// available, letting unavailable features degrade gracefully.
type Capabilities struct {
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	PTY      bool   `json:"pty"`
	Desktop  bool   `json:"desktop"`
	Exec     bool   `json:"exec"`
	File     bool   `json:"file"`
	Hostname string `json:"hostname"`
	Version  string `json:"version"`
}

// WriteFrame writes one typed frame.
func WriteFrame(w io.Writer, t FrameType, payload []byte) error {
	if len(payload) > MaxFramePayload {
		return fmt.Errorf("protocol: frame payload too large (%d bytes)", len(payload))
	}
	var hdr [5]byte
	hdr[0] = byte(t)
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

// ReadFrame reads one typed frame. It rejects oversized payloads instead of
// allocating attacker-controlled amounts of memory.
func ReadFrame(r io.Reader) (FrameType, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	if n > MaxFramePayload {
		return 0, nil, fmt.Errorf("protocol: oversized frame (%d bytes)", n)
	}
	payload := make([]byte, n)
	if n > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return 0, nil, err
		}
	}
	return FrameType(hdr[0]), payload, nil
}

// WriteJSON marshals v and writes it as a FrameJSON frame.
func WriteJSON(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("protocol: marshal: %w", err)
	}
	return WriteFrame(w, FrameJSON, b)
}

// WriteOpen writes the mandatory Open frame.
func WriteOpen(w io.Writer, o Open) error { return WriteJSON(w, o) }

// WriteExit writes a process-exit report as a FrameExit frame.
func WriteExit(w io.Writer, e Exit) error {
	b, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("protocol: marshal exit: %w", err)
	}
	return WriteFrame(w, FrameExit, b)
}

// ReadOpen reads and decodes the mandatory Open frame.
func ReadOpen(r io.Reader) (Open, error) {
	t, payload, err := ReadFrame(r)
	if err != nil {
		return Open{}, err
	}
	if t != FrameJSON {
		return Open{}, fmt.Errorf("protocol: expected open frame, got type %d", t)
	}
	var o Open
	if err := json.Unmarshal(payload, &o); err != nil {
		return Open{}, fmt.Errorf("protocol: decode open: %w", err)
	}
	if o.Kind == "" {
		return Open{}, fmt.Errorf("protocol: open frame missing kind")
	}
	return o, nil
}
