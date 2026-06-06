package session

import (
	"encoding/json"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/mirkobrombin/remolo/internal/protocol"
	"github.com/mirkobrombin/remolo/internal/pty"
	"github.com/mirkobrombin/remolo/internal/transport"
)

const copyBuf = 32 * 1024

// servePTY allocates a pseudo-terminal, then bridges it to the channel stream:
// PTY output becomes FrameData, inbound FrameData becomes PTY input, and
// FrameResize adjusts the window. On exit it sends a FrameExit with the code.
func (h *Host) servePTY(o protocol.Open, stream transport.Stream, st *connState) {
	if !h.scope.allowsCmd(o.Command) {
		_ = protocol.WriteExit(stream, protocol.Exit{Code: -1, Err: "command not permitted by this token"})
		return
	}
	// Roaming: a PTY with a ResumeID is detachable and survives reconnects.
	if o.ResumeID != "" {
		h.serveResumablePTY(o, stream)
		return
	}
	p, err := pty.Start(pty.Config{
		Command: o.Command,
		Term:    o.Term,
		Cols:    o.Cols,
		Rows:    o.Rows,
		Dir:     o.Dir,
		Env:     withAgentSock(envSlice(o.Env), st),
	})
	if err != nil {
		_ = protocol.WriteExit(stream, protocol.Exit{Code: -1, Err: err.Error()})
		return
	}

	// Inbound pump: client -> PTY (stdin, resize).
	go func() {
		for {
			ft, payload, err := protocol.ReadFrame(stream)
			if err != nil {
				p.Close()
				return
			}
			switch ft {
			case protocol.FrameData:
				if _, err := p.Write(payload); err != nil {
					return
				}
			case protocol.FrameResize:
				var r protocol.Resize
				if json.Unmarshal(payload, &r) == nil {
					_ = p.Resize(r.Cols, r.Rows)
				}
			case protocol.FrameEOF:
				// The client finished sending input. Do NOT close the PTY:
				// that would kill the shell mid-command. The shell exits on its
				// own (e.g. `exit`); a full channel close is handled by the
				// ReadFrame error path above, which tears the PTY down.
			}
		}
	}()

	// Outbound pump: PTY -> client (stdout). Runs until the shell exits.
	buf := make([]byte, copyBuf)
	for {
		n, err := p.Read(buf)
		if n > 0 {
			if werr := protocol.WriteFrame(stream, protocol.FrameData, buf[:n]); werr != nil {
				break
			}
		}
		if err != nil {
			break
		}
	}

	code, _ := p.Wait()
	_ = protocol.WriteExit(stream, protocol.Exit{Code: code})
}

// serveExec runs a one-shot command without a PTY, streaming combined output
// back as FrameData and reporting the exit code.
// withAgentSock appends SSH_AUTH_SOCK to env when agent forwarding is enabled.
func withAgentSock(env []string, st *connState) []string {
	if st != nil {
		if sock := st.authSock(); sock != "" {
			env = append(env, "SSH_AUTH_SOCK="+sock)
		}
	}
	return env
}

func (h *Host) serveExec(o protocol.Open, stream transport.Stream, st *connState) {
	if len(o.Command) == 0 {
		_ = protocol.WriteExit(stream, protocol.Exit{Code: -1, Err: "exec: empty command"})
		return
	}
	if !h.scope.allowsCmd(o.Command) {
		_ = protocol.WriteExit(stream, protocol.Exit{Code: -1, Err: "command not permitted by this token"})
		return
	}
	cmd := exec.Command(o.Command[0], o.Command[1:]...)
	cmd.Env = append(cmd.Environ(), withAgentSock(envSlice(o.Env), st)...)
	if o.Dir != "" {
		cmd.Dir = o.Dir
	}

	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	if err := cmd.Start(); err != nil {
		_ = protocol.WriteExit(stream, protocol.Exit{Code: -1, Err: err.Error()})
		return
	}

	var wg sync.WaitGroup
	var mu sync.Mutex // serialise frame writes from stdout+stderr
	// stdout and stderr ride separate frame types so the client can keep them
	// apart (SSH-like behaviour for scripting).
	pump := func(r io.Reader, ft protocol.FrameType) {
		defer wg.Done()
		b := make([]byte, copyBuf)
		for {
			n, err := r.Read(b)
			if n > 0 {
				mu.Lock()
				_ = protocol.WriteFrame(stream, ft, b[:n])
				mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}
	wg.Add(2)
	go pump(stdout, protocol.FrameData)
	go pump(stderr, protocol.FrameStderr)

	// Inbound stdin from the client.
	go func() {
		for {
			ft, payload, err := protocol.ReadFrame(stream)
			if err != nil {
				stdin.Close()
				return
			}
			switch ft {
			case protocol.FrameData:
				_, _ = stdin.Write(payload)
			case protocol.FrameEOF:
				stdin.Close()
				return
			}
		}
	}()

	wg.Wait()
	code := 0
	if err := cmd.Wait(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			code = -1
		}
	}
	_ = protocol.WriteExit(stream, protocol.Exit{Code: code})
}

func envSlice(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// authLimiter throttles repeated failed handshakes per remote address to blunt
// brute-force attempts against the PSK.
type authLimiter struct {
	mu       sync.Mutex
	attempts map[string]*attemptRecord
}

type attemptRecord struct {
	count int
	last  time.Time
}

func newAuthLimiter() *authLimiter {
	return &authLimiter{attempts: map[string]*attemptRecord{}}
}

const (
	maxAuthAttempts = 5
	authWindow      = 30 * time.Second
)

func (l *authLimiter) allow(remote string) bool {
	host := stripPort(remote)
	l.mu.Lock()
	defer l.mu.Unlock()
	rec := l.attempts[host]
	now := time.Now()
	if rec == nil {
		l.attempts[host] = &attemptRecord{count: 1, last: now}
		return true
	}
	if now.Sub(rec.last) > authWindow {
		rec.count = 1
		rec.last = now
		return true
	}
	rec.count++
	rec.last = now
	return rec.count <= maxAuthAttempts
}

func (l *authLimiter) reset(remote string) {
	host := stripPort(remote)
	l.mu.Lock()
	delete(l.attempts, host)
	l.mu.Unlock()
}

func stripPort(addr string) string {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[:i]
		}
	}
	return addr
}
