// Package transport defines remolo's pluggable connection layer and the
// happy-eyeballs selector that drives the "degrade in a good way" ladder.
//
// Every strategy (direct QUIC and TCP, mDNS, hole-punch, relay, SSH) is reduced
// to an Attempt: a labelled dial function with a quality hint. The selector
// races attempts in parallel with a small stagger and keeps the first
// connection to come up, reporting every attempt so the degradation sequence
// is observable to the user.
package transport

import (
	"context"
	"io"
	"net"
	"time"
)

// Stream is a bidirectional, reliable, ordered byte stream (a QUIC stream).
type Stream interface {
	io.ReadWriteCloser
	SetDeadline(t time.Time) error
}

// Conn is an established, encrypted, multiplexed connection to a peer. Logical
// channels are layered on top by opening/accepting streams.
type Conn interface {
	// OpenStream opens a new outbound stream, blocking until the peer grants
	// flow-control credit or ctx is done.
	OpenStream(ctx context.Context) (Stream, error)
	// AcceptStream accepts the next inbound stream.
	AcceptStream(ctx context.Context) (Stream, error)
	// RemoteAddr is the peer's network address.
	RemoteAddr() net.Addr
	// Close tears the connection down with a human-readable reason.
	Close(reason string) error
}

// QualityHint orders strategies on the degradation ladder: lower is better
// (less latency, more control).
type QualityHint int

const (
	QualityDirect    QualityHint = iota // same LAN/routed, minimal latency
	QualityLocalDisc                    // mDNS / zeroconf on the local segment
	QualityHolePunch                    // NAT traversal, low latency
	QualityRelay                        // forwarded via server, medium latency
	QualitySSH                          // tunnelled through sshd
	QualityManual                       // operator supplied an address by hand
)

func (q QualityHint) String() string {
	switch q {
	case QualityDirect:
		return "direct"
	case QualityLocalDisc:
		return "mDNS"
	case QualityHolePunch:
		return "hole-punch"
	case QualityRelay:
		return "relay"
	case QualitySSH:
		return "ssh"
	case QualityManual:
		return "manual"
	default:
		return "unknown"
	}
}
