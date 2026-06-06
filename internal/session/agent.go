package session

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"

	"github.com/mirkobrombin/remolo/internal/mux"
	"github.com/mirkobrombin/remolo/internal/protocol"
	"github.com/mirkobrombin/remolo/internal/transport"
	"github.com/mirkobrombin/remolo/internal/tunnel"
)

// connState is per-connection host state shared between the control handler and
// the channel handlers (currently just SSH agent forwarding).
type connState struct {
	ctx       context.Context
	mux       *mux.Mux
	log       Logger
	mu        sync.Mutex
	agentSock string       // SSH_AUTH_SOCK path exposed in the remote env
	agentLn   net.Listener // listener on that socket
}

func (s *connState) authSock() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.agentSock
}

func (s *connState) close() {
	s.mu.Lock()
	ln := s.agentLn
	sock := s.agentSock
	s.mu.Unlock()
	if ln != nil {
		ln.Close()
	}
	if sock != "" {
		os.Remove(sock)
	}
}

// enableAgentForward sets up a host-side unix socket that proxies to the
// client's SSH agent. The remote shell sees it via SSH_AUTH_SOCK; every
// connection to it is relayed back to the client over a host-initiated KindAgent
// channel, where the client splices it to its real agent. This is ssh -A.
func (h *Host) enableAgentForward(st *connState) (string, error) {
	st.mu.Lock()
	if st.agentSock != "" {
		sock := st.agentSock
		st.mu.Unlock()
		return sock, nil
	}
	st.mu.Unlock()

	dir, err := os.MkdirTemp("", "remolo-agent-")
	if err != nil {
		return "", err
	}
	sock := filepath.Join(dir, "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	st.mu.Lock()
	st.agentSock = sock
	st.agentLn = ln
	st.mu.Unlock()

	go h.acceptAgentConns(st, ln)
	return sock, nil
}

// acceptAgentConns relays each local agent-socket connection to the client.
func (h *Host) acceptAgentConns(st *connState, ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			stream, err := st.mux.Open(st.ctx, protocol.Open{Kind: protocol.KindAgent})
			if err != nil {
				c.Close()
				return
			}
			tunnel.Splice(c, stream)
		}(c)
	}
}

// serveControlState handles control-channel keepalives and post-handshake
// control messages (such as enabling agent forwarding).
func (h *Host) serveControlState(ctrl transport.Stream, st *connState) {
	for {
		ft, payload, err := protocol.ReadFrame(ctrl)
		if err != nil {
			return
		}
		switch ft {
		case protocol.FramePing:
			_ = protocol.WriteFrame(ctrl, protocol.FramePong, nil)
		case protocol.FrameJSON:
			var msg protocol.ControlMsg
			if json.Unmarshal(payload, &msg) != nil {
				continue
			}
			if msg.Type == "agent-forward" {
				ack := protocol.ControlMsg{Type: "agent-forward-ok"}
				if _, err := h.enableAgentForward(st); err != nil {
					ack.Type = "agent-forward-fail"
				}
				_ = protocol.WriteJSON(ctrl, ack)
			}
		}
	}
}
