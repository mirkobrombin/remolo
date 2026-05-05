// Package tcpmux provides a multiplexed remolo transport over any reliable
// byte stream: a plain TCP connection, a connection forwarded through the
// relay, or a channel tunnelled through sshd. It wraps the underlying
// net.Conn with a yamux session and adapts it to transport.Conn so the rest
// of remolo can layer logical channels (a shell, a desktop, a file transfer)
// on top exactly as it does over QUIC.
//
// The byte stream below this layer is expected to already be encrypted
// end-to-end (TLS for direct TCP, or the peer-to-peer crypto handshake for a
// relayed stream); yamux here only provides framing and multiplexing.
package tcpmux

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"

	"github.com/hashicorp/yamux"
	"github.com/mirkobrombin/remolo/internal/transport"
)

// muxConfig returns a yamux config tuned for remolo. Keep-alives detect dead
// peers and the log output is silenced (sent to io.Discard) so the library
// does not write directly to stderr.
func muxConfig() *yamux.Config {
	c := yamux.DefaultConfig()
	c.LogOutput = io.Discard
	c.Logger = nil
	return c
}

// muxConn adapts a *yamux.Session to the transport.Conn interface. The raw
// net.Conn is retained only to report the peer's network address.
type muxConn struct {
	sess *yamux.Session
	raw  net.Conn
}

func (m *muxConn) OpenStream(ctx context.Context) (transport.Stream, error) {
	type result struct {
		s   *yamux.Stream
		err error
	}
	ch := make(chan result, 1)
	go func() {
		s, err := m.sess.OpenStream()
		ch <- result{s: s, err: err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		return r.s, nil
	}
}

func (m *muxConn) AcceptStream(ctx context.Context) (transport.Stream, error) {
	s, err := m.sess.AcceptStreamWithContext(ctx)
	if err != nil {
		return nil, err
	}
	return s, nil
}

func (m *muxConn) RemoteAddr() net.Addr { return m.raw.RemoteAddr() }

// Close shuts down the yamux session (which closes the underlying conn). The
// reason is accepted for interface symmetry; yamux has no field for it.
func (m *muxConn) Close(reason string) error {
	_ = reason
	return m.sess.Close()
}

// NewClientConn wraps an already-established net.Conn with a yamux client
// session and returns it as a transport.Conn. The peer must wrap its end with
// NewServerConn.
func NewClientConn(raw net.Conn) (transport.Conn, error) {
	sess, err := yamux.Client(raw, muxConfig())
	if err != nil {
		return nil, fmt.Errorf("tcpmux client session: %w", err)
	}
	return &muxConn{sess: sess, raw: raw}, nil
}

// NewServerConn wraps an already-established net.Conn with a yamux server
// session and returns it as a transport.Conn. The peer must wrap its end with
// NewClientConn.
func NewServerConn(raw net.Conn) (transport.Conn, error) {
	sess, err := yamux.Server(raw, muxConfig())
	if err != nil {
		return nil, fmt.Errorf("tcpmux server session: %w", err)
	}
	return &muxConn{sess: sess, raw: raw}, nil
}

// DialTCP dials addr over TCP+TLS using the supplied (pinned) TLS config and
// returns a multiplexed transport.Conn as the yamux client side.
func DialTCP(ctx context.Context, addr string, tlsConf *tls.Config) (transport.Conn, error) {
	d := &tls.Dialer{Config: tlsConf}
	raw, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("tcpmux dial %s: %w", addr, err)
	}
	conn, err := NewClientConn(raw)
	if err != nil {
		raw.Close()
		return nil, err
	}
	return conn, nil
}

// Listener accepts inbound TCP+TLS connections and turns each into a
// multiplexed transport.Conn on the yamux server side.
type Listener struct {
	l net.Listener
}

// ListenTCP starts a TCP listener bound to addr (e.g. "0.0.0.0:0") wrapped in
// the supplied server TLS config. The chosen local address is available via
// Addr.
func ListenTCP(addr string, tlsConf *tls.Config) (*Listener, error) {
	raw, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("tcpmux listen %s: %w", addr, err)
	}
	l := tls.NewListener(raw, tlsConf)
	return &Listener{l: l}, nil
}

// Accept blocks for the next inbound connection and wraps it as a server-side
// transport.Conn. The ctx is honoured by aborting the accept when it is done.
func (l *Listener) Accept(ctx context.Context) (transport.Conn, error) {
	type result struct {
		c   net.Conn
		err error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := l.l.Accept()
		ch <- result{c: c, err: err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		conn, err := NewServerConn(r.c)
		if err != nil {
			r.c.Close()
			return nil, err
		}
		return conn, nil
	}
}

// Addr returns the local address the listener is bound to.
func (l *Listener) Addr() net.Addr { return l.l.Addr() }

// Close stops accepting and closes the listener.
func (l *Listener) Close() error { return l.l.Close() }
