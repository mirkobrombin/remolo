package session

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"time"

	"github.com/mirkobrombin/remolo/internal/crypto"
	"github.com/mirkobrombin/remolo/internal/protocol"
	"github.com/mirkobrombin/remolo/internal/token"
	"github.com/mirkobrombin/remolo/internal/transport"
	"github.com/mirkobrombin/remolo/internal/transport/tcpmux"
)

// ConnectViaJump reaches targetTok through an intermediate bastion (jumpTok),
// SSH ProxyJump style. It connects to the bastion, asks it to forward to the
// target's TCP endpoint, then runs the full remolo handshake with the target
// over that forwarded byte stream. End-to-end encryption is preserved: the
// bastion only relays ciphertext.
func ConnectViaJump(ctx context.Context, targetTok, jumpTok *token.Token) (*Client, error) {
	jump, err := ConnectWith(ctx, jumpTok, ConnectOptions{}, nil)
	if err != nil {
		return nil, fmt.Errorf("jump host: %w", err)
	}

	tlsConf := crypto.ClientTLSConfig(targetTok.HostPubKey)
	var lastErr error
	for _, cand := range targetTok.Candidates {
		fwd, ferr := jump.OpenChannel(ctx, protocol.Open{Kind: protocol.KindForward, Target: cand.Addr()})
		if ferr != nil {
			lastErr = ferr
			continue
		}
		nc := &streamConn{Stream: fwd}
		tlsConn := tls.Client(nc, tlsConf)
		if herr := tlsConn.HandshakeContext(ctx); herr != nil {
			lastErr = herr
			fwd.Close()
			continue
		}
		conn, cerr := tcpmux.NewClientConn(tlsConn)
		if cerr != nil {
			lastErr = cerr
			fwd.Close()
			continue
		}
		cl, herr := handshakeClient(ctx, conn, targetTok)
		if herr != nil {
			lastErr = herr
			conn.Close("handshake failed")
			continue
		}
		cl.route = "jump via " + jump.Route()
		cl.onClose = func() { jump.Close("jump ended") }
		return cl, nil
	}
	jump.Close("jump unused")
	if lastErr == nil {
		lastErr = fmt.Errorf("target has no candidates reachable from the jump host")
	}
	return nil, lastErr
}

// streamConn adapts a transport.Stream to net.Conn so a TLS client can run over
// a forwarded mux channel. The addresses are synthetic; only the byte stream
// and deadlines matter.
type streamConn struct {
	transport.Stream
}

type jumpAddr struct{}

func (jumpAddr) Network() string { return "remolo-jump" }
func (jumpAddr) String() string  { return "remolo-jump" }

func (c *streamConn) LocalAddr() net.Addr                { return jumpAddr{} }
func (c *streamConn) RemoteAddr() net.Addr               { return jumpAddr{} }
func (c *streamConn) SetReadDeadline(t time.Time) error  { return c.Stream.SetDeadline(t) }
func (c *streamConn) SetWriteDeadline(t time.Time) error { return c.Stream.SetDeadline(t) }
