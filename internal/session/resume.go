package session

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/mirkobrombin/remolo/internal/protocol"
	"github.com/mirkobrombin/remolo/internal/pty"
	"github.com/mirkobrombin/remolo/internal/transport"
)

// Roaming (mosh-grade): a PTY opened with a ResumeID keeps running on the host
// even if the client's transport drops (network change, suspend). The shell is
// detached, its recent output buffered in a ring; when the client reconnects
// with the same ResumeID the host replays the buffer and reattaches the live
// stream. A detached session is reaped after an idle timeout so a vanished
// client cannot leave a zombie shell forever.

const (
	resumeRingSize    = 256 * 1024 // bytes of scrollback replayed on reattach
	resumeIdleTimeout = 10 * time.Minute
)

type resumeRegistry struct {
	mu       sync.Mutex
	sessions map[string]*ptySession
}

var resumes = &resumeRegistry{sessions: map[string]*ptySession{}}

// getOrCreate returns the existing session for id, or creates one (starting the
// shell and its output pump). isNew reports whether it was created.
func (r *resumeRegistry) getOrCreate(id string, o protocol.Open) (*ptySession, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.sessions[id]; ok {
		return s, false, nil
	}
	p, err := pty.Start(pty.Config{Command: o.Command, Term: o.Term, Cols: o.Cols, Rows: o.Rows, Dir: o.Dir, Env: envSlice(o.Env)})
	if err != nil {
		return nil, false, err
	}
	s := &ptySession{p: p, id: id}
	r.sessions[id] = s
	go s.pump()
	return s, true, nil
}

func (r *resumeRegistry) remove(id string) {
	r.mu.Lock()
	delete(r.sessions, id)
	r.mu.Unlock()
}

// ptySession is a detachable shell.
type ptySession struct {
	p  pty.PTY
	id string

	mu       sync.Mutex
	ring     []byte
	stream   transport.Stream
	exited   bool
	exitCode int
	idle     *time.Timer
}

// pump reads the shell's output forever: it buffers into the ring and forwards
// to the currently-attached stream (if any). When the shell exits it notifies
// the attached stream and removes the session.
func (s *ptySession) pump() {
	buf := make([]byte, 32*1024)
	for {
		n, err := s.p.Read(buf)
		if n > 0 {
			s.mu.Lock()
			s.appendRing(buf[:n])
			if s.stream != nil {
				_ = protocol.WriteFrame(s.stream, protocol.FrameData, buf[:n])
			}
			s.mu.Unlock()
		}
		if err != nil {
			break
		}
	}
	code, _ := s.p.Wait()
	s.mu.Lock()
	s.exited = true
	s.exitCode = code
	if s.stream != nil {
		_ = protocol.WriteExit(s.stream, protocol.Exit{Code: code})
	}
	if s.idle != nil {
		s.idle.Stop()
	}
	s.mu.Unlock()
	resumes.remove(s.id)
}

func (s *ptySession) appendRing(b []byte) {
	s.ring = append(s.ring, b...)
	if len(s.ring) > resumeRingSize {
		s.ring = s.ring[len(s.ring)-resumeRingSize:]
	}
}

// attach replays buffered output to stream, makes it the live stream, then runs
// the input pump until the stream drops. On drop the session detaches (the shell
// keeps running) and an idle reaper is armed.
func (s *ptySession) attach(stream transport.Stream) {
	s.mu.Lock()
	if s.idle != nil {
		s.idle.Stop()
		s.idle = nil
	}
	if len(s.ring) > 0 {
		_ = protocol.WriteFrame(stream, protocol.FrameData, s.ring)
	}
	if s.exited {
		_ = protocol.WriteExit(stream, protocol.Exit{Code: s.exitCode})
		s.mu.Unlock()
		return
	}
	s.stream = stream
	s.mu.Unlock()

	// Input pump: client -> shell, until the stream errors (disconnect).
	for {
		ft, payload, err := protocol.ReadFrame(stream)
		if err != nil {
			break
		}
		switch ft {
		case protocol.FrameData:
			if _, werr := s.p.Write(payload); werr != nil {
				break
			}
		case protocol.FrameResize:
			var rz protocol.Resize
			if json.Unmarshal(payload, &rz) == nil {
				_ = s.p.Resize(rz.Cols, rz.Rows)
			}
		}
	}

	// Detached: keep the shell alive, arm the idle reaper.
	s.mu.Lock()
	if s.stream == stream {
		s.stream = nil
	}
	if !s.exited && s.idle == nil {
		s.idle = time.AfterFunc(resumeIdleTimeout, func() { s.p.Close() })
	}
	s.mu.Unlock()
}

// serveResumablePTY handles a PTY channel that carries a ResumeID.
func (h *Host) serveResumablePTY(o protocol.Open, stream transport.Stream) {
	s, _, err := resumes.getOrCreate(o.ResumeID, o)
	if err != nil {
		_ = protocol.WriteExit(stream, protocol.Exit{Code: -1, Err: err.Error()})
		return
	}
	s.attach(stream)
}
