package session

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mirkobrombin/remolo/internal/crypto"
	"github.com/mirkobrombin/remolo/internal/identity"
	"github.com/mirkobrombin/remolo/internal/mux"
	"github.com/mirkobrombin/remolo/internal/protocol"
	"github.com/mirkobrombin/remolo/internal/token"
	"github.com/mirkobrombin/remolo/internal/transport"
)

// Client is the controlling side: an authenticated, multiplexed connection to a
// host over which logical channels (shell, exec, ...) are opened on demand.
type Client struct {
	mux     *mux.Mux
	ctrl    transport.Stream
	caps    protocol.Capabilities
	key     []byte
	route   string
	closed  bool
	onClose func() // extra teardown (e.g. a jump-host connection)
}

// ConnectOptions enables the lower rungs of the degradation ladder, which are
// off by default because they need extra inputs (a relay address, SSH creds) or
// emit network chatter (mDNS browse).
type ConnectOptions struct {
	MDNS       bool          // browse the local segment for the host
	Rendezvous string        // broker base URL to look the host up by session id
	Relay      string        // relay server address for the relay rung
	SSH        *SSHRung      // SSH tunnel fallback, if configured
	Stagger    time.Duration // happy-eyeballs stagger (0 uses the default)
}

// SSHRung configures the SSH tunnel transport.
type SSHRung struct {
	Addr       string // sshd address host:port
	User       string
	KeyPath    string // path to a private key
	KnownHosts string // known_hosts path (empty = insecure accept, dev only)
	Target     string // host:port of the remolo TCP endpoint reachable from the sshd
}

// Connect dials the host described by tok using only the direct rungs (QUIC and
// TCP over every candidate), racing them with happy-eyeballs.
func Connect(ctx context.Context, tok *token.Token, onResult func(transport.Result)) (*Client, error) {
	return ConnectWith(ctx, tok, ConnectOptions{}, onResult)
}

// ConnectWith dials the host using the full degradation ladder permitted by
// opts: direct QUIC and TCP per candidate, mDNS discovery, rendezvous lookup,
// relay and SSH fallback. It races them by quality (best first) with a stagger,
// reports each attempt to onResult, then authenticates and reads capabilities.
func ConnectWith(ctx context.Context, tok *token.Token, opts ConnectOptions, onResult func(transport.Result)) (*Client, error) {
	if tok.Expired() {
		return nil, fmt.Errorf("token expired %s ago", (-tok.ExpiresIn()).Round(time.Second))
	}
	attempts := buildAttempts(ctx, tok, opts)

	stagger := opts.Stagger
	if stagger <= 0 {
		stagger = transport.DefaultStagger
	}
	conn, route, err := transport.Race(ctx, attempts, stagger, onResult)
	if err != nil {
		return nil, err
	}

	cl, err := handshakeClient(ctx, conn, tok)
	if err != nil {
		conn.Close("handshake failed")
		return nil, err
	}
	cl.route = route
	return cl, nil
}

// ConnectKey connects to a host described by its public key and candidates,
// authenticating with an enrolled client key instead of a token.
func ConnectKey(ctx context.Context, hostPub []byte, candidates []token.Candidate, clientPriv ed25519.PrivateKey, clientPub ed25519.PublicKey, onResult func(transport.Result)) (*Client, error) {
	synthetic := &token.Token{HostPubKey: hostPub, Candidates: candidates}
	attempts := buildAttempts(ctx, synthetic, ConnectOptions{})
	conn, route, err := transport.Race(ctx, attempts, transport.DefaultStagger, onResult)
	if err != nil {
		return nil, err
	}
	cl, err := handshakeClientKey(ctx, conn, hostPub, clientPriv, clientPub)
	if err != nil {
		conn.Close("handshake failed")
		return nil, err
	}
	cl.route = route
	return cl, nil
}

// handshakeClientKey opens the control channel and authenticates with a client
// key (the 'K' method), then reads capabilities.
func handshakeClientKey(ctx context.Context, conn transport.Conn, hostPub []byte, clientPriv ed25519.PrivateKey, clientPub ed25519.PublicKey) (*Client, error) {
	m := mux.New(conn)
	ctrl, err := m.Open(ctx, protocol.Open{Kind: protocol.KindControl})
	if err != nil {
		return nil, fmt.Errorf("open control channel: %w", err)
	}
	if _, err := ctrl.Write([]byte{'K'}); err != nil {
		return nil, err
	}
	if err := identity.ClientAuth(ctrl, clientPriv, clientPub, ed25519.PublicKey(hostPub)); err != nil {
		return nil, err
	}
	caps, err := readCaps(ctrl)
	if err != nil {
		return nil, err
	}
	return &Client{mux: m, ctrl: ctrl, caps: caps}, nil
}

func readCaps(ctrl transport.Stream) (protocol.Capabilities, error) {
	ft, payload, err := protocol.ReadFrame(ctrl)
	if err != nil {
		return protocol.Capabilities{}, fmt.Errorf("read capabilities: %w", err)
	}
	if ft != protocol.FrameJSON {
		return protocol.Capabilities{}, fmt.Errorf("expected capabilities frame, got type %d", ft)
	}
	var caps protocol.Capabilities
	if err := json.Unmarshal(payload, &caps); err != nil {
		return protocol.Capabilities{}, fmt.Errorf("decode capabilities: %w", err)
	}
	return caps, nil
}

// handshakeClient opens the control channel, authenticates and exchanges caps.
func handshakeClient(ctx context.Context, conn transport.Conn, tok *token.Token) (*Client, error) {
	m := mux.New(conn)
	ctrl, err := m.Open(ctx, protocol.Open{Kind: protocol.KindControl})
	if err != nil {
		return nil, fmt.Errorf("open control channel: %w", err)
	}
	// Select PSK authentication.
	if _, err := ctrl.Write([]byte{'P'}); err != nil {
		return nil, err
	}
	res, err := crypto.ClientHandshake(ctrl, tok.PSK, tok.HostPubKey)
	if err != nil {
		return nil, err
	}
	ft, payload, err := protocol.ReadFrame(ctrl)
	if err != nil {
		return nil, fmt.Errorf("read capabilities: %w", err)
	}
	if ft != protocol.FrameJSON {
		return nil, fmt.Errorf("expected capabilities frame, got type %d", ft)
	}
	var caps protocol.Capabilities
	if err := json.Unmarshal(payload, &caps); err != nil {
		return nil, fmt.Errorf("decode capabilities: %w", err)
	}
	return &Client{mux: m, ctrl: ctrl, caps: caps, key: res.SessionKey}, nil
}

// Capabilities returns what the host advertised.
func (c *Client) Capabilities() protocol.Capabilities { return c.caps }

// Route returns the label of the winning connection strategy.
func (c *Client) Route() string { return c.route }

// OpenChannel opens a new logical channel of the given Open descriptor.
func (c *Client) OpenChannel(ctx context.Context, o protocol.Open) (transport.Stream, error) {
	return c.mux.Open(ctx, o)
}

// AcceptChannel accepts a host-initiated channel (e.g. SSH agent forwarding).
func (c *Client) AcceptChannel(ctx context.Context) (protocol.Open, transport.Stream, error) {
	return c.mux.Accept(ctx)
}

// RequestAgentForward asks the host to expose this client's SSH agent to the
// remote shell (ssh -A). It blocks for the host's acknowledgement.
func (c *Client) RequestAgentForward() error {
	if err := protocol.WriteJSON(c.ctrl, protocol.ControlMsg{Type: "agent-forward"}); err != nil {
		return err
	}
	ft, payload, err := protocol.ReadFrame(c.ctrl)
	if err != nil {
		return err
	}
	if ft != protocol.FrameJSON {
		return fmt.Errorf("unexpected control reply")
	}
	var msg protocol.ControlMsg
	if err := json.Unmarshal(payload, &msg); err != nil {
		return err
	}
	if msg.Type != "agent-forward-ok" {
		return fmt.Errorf("host declined agent forwarding")
	}
	return nil
}

// Conn exposes the underlying transport connection (used by the mux daemon).
func (c *Client) Conn() transport.Conn { return c.mux.Conn() }

// Mux exposes the multiplexer (used by the mux daemon to open channels).
func (c *Client) Mux() *mux.Mux { return c.mux }

// Close tears down the connection.
func (c *Client) Close(reason string) error {
	if c.closed {
		return nil
	}
	c.closed = true
	err := c.mux.Close(reason)
	if c.onClose != nil {
		c.onClose()
	}
	return err
}
