// Package relay implements an end-to-end preserving relay for remolo, used as
// a fallback when neither a direct connection nor NAT hole-punching succeeds.
//
// The relay is deliberately dumb: it never observes plaintext. A host and a
// client that share a session id each connect to the relay and announce their
// role with a single greeting line. The relay pairs a waiting host with a
// client of the same session id and then copies opaque bytes between the two
// raw connections in both directions until one side closes. All encryption and
// multiplexing happens above this layer, between the two peers themselves
// (TLS or the remolo peer-to-peer crypto handshake, plus yamux via the tcpmux
// package).
package relay

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/mirkobrombin/remolo/internal/transport"
	"github.com/mirkobrombin/remolo/internal/transport/tcpmux"
)

// greetingPrefix is the fixed token that begins every relay greeting line.
const greetingPrefix = "REMOLO-RELAY"

// maxGreetingLen bounds the greeting line so a misbehaving peer cannot make
// the relay buffer without limit.
const maxGreetingLen = 512

// greetTimeout bounds how long the relay waits for a peer to send its
// greeting before giving up on the connection.
const greetTimeout = 30 * time.Second

// Server pairs hosts and clients by session id and relays bytes between them.
// The zero value is not usable; construct one with NewServer.
type Server struct {
	mu      sync.Mutex
	waiting map[string]net.Conn // session id -> first peer to arrive (any role)
}

// NewServer returns a ready-to-use relay Server.
func NewServer() *Server {
	return &Server{waiting: make(map[string]net.Conn)}
}

// parkTimeout bounds how long the first peer waits for its counterpart before
// the relay gives up and closes it.
const parkTimeout = 2 * time.Minute

// Serve accepts connections on ln and relays them until ln is closed. It
// returns the accept error (typically from ln.Close) when it stops.
func (s *Server) Serve(ln net.Listener) error {
	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		go s.handle(c)
	}
}

// handle reads a peer's greeting and pairs it with the matching session. The
// matching is arrival-order independent: whichever of the host or client
// connects first is parked under the session id until its counterpart arrives,
// at which point the two are spliced together. This removes any requirement
// that the host be listening before the client dials.
func (s *Server) handle(c net.Conn) {
	_, key, err := readGreeting(c)
	if err != nil {
		c.Close()
		return
	}

	s.mu.Lock()
	if partner, ok := s.waiting[key]; ok {
		// Second peer for this id: pair them.
		delete(s.waiting, key)
		s.mu.Unlock()
		splice(partner, c)
		return
	}
	// First peer for this id: park it and wait for the counterpart.
	s.waiting[key] = c
	s.mu.Unlock()

	// Reap the parked connection if no counterpart shows up in time.
	go func() {
		time.Sleep(parkTimeout)
		s.mu.Lock()
		if cur, ok := s.waiting[key]; ok && cur == c {
			delete(s.waiting, key)
			s.mu.Unlock()
			c.Close()
			return
		}
		s.mu.Unlock()
	}()
}

// splice copies bytes bidirectionally between two connections until either
// direction ends, then closes both.
func splice(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		io.Copy(dst, src)
		// Unblock the opposite direction by closing both ends.
		a.Close()
		b.Close()
	}
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
}

// readGreeting reads and parses the single greeting line that every peer sends
// first: "REMOLO-RELAY <role> <key>\n".
func readGreeting(c net.Conn) (role, key string, err error) {
	_ = c.SetReadDeadline(time.Now().Add(greetTimeout))
	// Read one byte at a time up to the newline. We must NOT buffer past the
	// greeting: any following bytes belong to the peers' end-to-end handshake
	// (TLS ClientHello, yamux) and the relay must forward them verbatim, not
	// swallow them into a bufio buffer.
	var line []byte
	buf := make([]byte, 1)
	for len(line) < maxGreetingLen {
		n, rerr := c.Read(buf)
		if n > 0 {
			if buf[0] == '\n' {
				break
			}
			line = append(line, buf[0])
		}
		if rerr != nil {
			return "", "", fmt.Errorf("relay greeting: %w", rerr)
		}
	}
	// Clear the read deadline for the relay phase.
	_ = c.SetReadDeadline(time.Time{})
	fields := strings.Fields(strings.TrimSpace(string(line)))
	if len(fields) != 3 || fields[0] != greetingPrefix {
		return "", "", fmt.Errorf("relay greeting: malformed %q", string(line))
	}
	return fields[1], fields[2], nil
}

// writeGreeting sends the greeting line for the given role and session id.
func writeGreeting(c net.Conn, role, sessionHex string) error {
	_, err := io.WriteString(c, fmt.Sprintf("%s %s %s\n", greetingPrefix, role, sessionHex))
	return err
}

// DialRelay connects to the relay as the client side of a session, performs
// the greeting, and returns a multiplexed transport.Conn established over the
// relayed stream. The tlsConf secures the peer-to-peer link end-to-end; the
// relay sees only ciphertext.
func DialRelay(ctx context.Context, relayAddr, sessionHex string, tlsConf *tls.Config) (transport.Conn, error) {
	var d net.Dialer
	raw, err := d.DialContext(ctx, "tcp", relayAddr)
	if err != nil {
		return nil, fmt.Errorf("relay dial %s: %w", relayAddr, err)
	}
	if err := writeGreeting(raw, "client", sessionHex); err != nil {
		raw.Close()
		return nil, fmt.Errorf("relay greeting: %w", err)
	}
	// The byte stream below tcpmux is opaque to the relay. When tlsConf is
	// supplied the client establishes TLS end-to-end with the host over the
	// relayed stream first; the host then wraps its raw conn with the matching
	// tls.Server. When tlsConf is nil the peers are expected to have already
	// secured the stream (e.g. via the remolo crypto handshake) above this
	// helper, so tcpmux runs directly on the relayed conn.
	var stream net.Conn = raw
	if tlsConf != nil {
		tlsConn := tls.Client(raw, tlsConf)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			raw.Close()
			return nil, fmt.Errorf("relay tls handshake: %w", err)
		}
		stream = tlsConn
	}
	conn, err := tcpmux.NewClientConn(stream)
	if err != nil {
		stream.Close()
		return nil, err
	}
	return conn, nil
}

// ListenRelay connects to the relay as the host side of a session and performs
// the greeting, returning the raw relayed conn. The caller is responsible for
// the end-to-end TLS (tls.Server) and wrapping the result with
// tcpmux.NewServerConn. Keeping the two steps separate lets the host install
// its own server TLS config. One relayed connection is established per call.
func ListenRelay(ctx context.Context, relayAddr, sessionHex string) (net.Conn, error) {
	var d net.Dialer
	raw, err := d.DialContext(ctx, "tcp", relayAddr)
	if err != nil {
		return nil, fmt.Errorf("relay dial %s: %w", relayAddr, err)
	}
	if err := writeGreeting(raw, "host", sessionHex); err != nil {
		raw.Close()
		return nil, fmt.Errorf("relay greeting: %w", err)
	}
	return raw, nil
}
