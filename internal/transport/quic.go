package transport

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"time"

	"github.com/quic-go/quic-go"
)

func quicConfig() *quic.Config {
	return &quic.Config{
		KeepAlivePeriod:      15 * time.Second,
		MaxIdleTimeout:       60 * time.Second,
		HandshakeIdleTimeout: 8 * time.Second,
	}
}

// quicConn adapts *quic.Conn to the transport.Conn interface.
type quicConn struct{ c *quic.Conn }

func (q quicConn) OpenStream(ctx context.Context) (Stream, error) {
	s, err := q.c.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	return s, nil
}

func (q quicConn) AcceptStream(ctx context.Context) (Stream, error) {
	s, err := q.c.AcceptStream(ctx)
	if err != nil {
		return nil, err
	}
	return s, nil
}

func (q quicConn) RemoteAddr() net.Addr { return q.c.RemoteAddr() }

func (q quicConn) Close(reason string) error {
	return q.c.CloseWithError(0, reason)
}

// DialQUIC dials a single endpoint over QUIC using the supplied (pinned) TLS
// config and returns a transport.Conn.
func DialQUIC(ctx context.Context, addr string, tlsConf *tls.Config) (Conn, error) {
	c, err := quic.DialAddr(ctx, addr, tlsConf, quicConfig())
	if err != nil {
		return nil, fmt.Errorf("quic dial %s: %w", addr, err)
	}
	return quicConn{c: c}, nil
}

// Listener accepts inbound QUIC connections on the host.
type Listener struct {
	l *quic.Listener
}

// ListenQUIC starts a QUIC listener bound to addr (e.g. "0.0.0.0:0") with the
// host's server TLS config. The chosen local address is available via Addr.
func ListenQUIC(addr string, tlsConf *tls.Config) (*Listener, error) {
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", addr, err)
	}
	pc, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, fmt.Errorf("listen udp %s: %w", addr, err)
	}
	l, err := quic.Listen(pc, tlsConf, quicConfig())
	if err != nil {
		pc.Close()
		return nil, fmt.Errorf("quic listen: %w", err)
	}
	return &Listener{l: l}, nil
}

// Accept blocks for the next inbound connection.
func (l *Listener) Accept(ctx context.Context) (Conn, error) {
	c, err := l.l.Accept(ctx)
	if err != nil {
		return nil, err
	}
	return quicConn{c: c}, nil
}

// Addr returns the local address the listener is bound to.
func (l *Listener) Addr() net.Addr { return l.l.Addr() }

// Port returns the UDP port the listener is bound to.
func (l *Listener) Port() int {
	if a, ok := l.l.Addr().(*net.UDPAddr); ok {
		return a.Port
	}
	return 0
}

// Close stops accepting and closes the listener.
func (l *Listener) Close() error { return l.l.Close() }
