// Package mux layers remolo's logical channels over a single transport
// connection. Each channel is one stream whose first frame declares its kind
// and parameters (see package protocol). Multiple channels (a shell, a desktop,
// a file transfer) run concurrently and independently over the same encrypted
// connection, which is what makes multi-session reuse cheap.
package mux

import (
	"context"

	"github.com/mirkobrombin/remolo/internal/protocol"
	"github.com/mirkobrombin/remolo/internal/transport"
)

// Mux multiplexes logical channels over one transport connection.
type Mux struct {
	conn transport.Conn
}

// New wraps an established connection.
func New(conn transport.Conn) *Mux { return &Mux{conn: conn} }

// Conn returns the underlying transport connection.
func (m *Mux) Conn() transport.Conn { return m.conn }

// Open starts a new outbound channel and writes its Open frame, so the peer's
// Accept can immediately learn the channel's kind and parameters.
func (m *Mux) Open(ctx context.Context, o protocol.Open) (transport.Stream, error) {
	s, err := m.conn.OpenStream(ctx)
	if err != nil {
		return nil, err
	}
	if err := protocol.WriteOpen(s, o); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// Accept blocks for the next inbound channel and returns its Open descriptor
// together with the stream.
func (m *Mux) Accept(ctx context.Context) (protocol.Open, transport.Stream, error) {
	s, err := m.conn.AcceptStream(ctx)
	if err != nil {
		return protocol.Open{}, nil, err
	}
	o, err := protocol.ReadOpen(s)
	if err != nil {
		s.Close()
		return protocol.Open{}, nil, err
	}
	return o, s, nil
}

// Close tears down the whole connection (all channels).
func (m *Mux) Close(reason string) error { return m.conn.Close(reason) }
