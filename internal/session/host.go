package session

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mirkobrombin/remolo/internal/crypto"
	"github.com/mirkobrombin/remolo/internal/desktop"
	"github.com/mirkobrombin/remolo/internal/dirsync"
	"github.com/mirkobrombin/remolo/internal/discovery"
	"github.com/mirkobrombin/remolo/internal/filetransfer"
	"github.com/mirkobrombin/remolo/internal/identity"
	"github.com/mirkobrombin/remolo/internal/mux"
	"github.com/mirkobrombin/remolo/internal/protocol"
	"github.com/mirkobrombin/remolo/internal/token"
	"github.com/mirkobrombin/remolo/internal/transport"
	"github.com/mirkobrombin/remolo/internal/transport/tcpmux"
	"github.com/mirkobrombin/remolo/internal/tunnel"
)

// HostOptions configures a host.
type HostOptions struct {
	Bind            string        // bind address, e.g. "0.0.0.0:0"
	TTL             time.Duration // token validity window
	IncludeLoopback bool          // include 127.0.0.1 in candidates (local testing)
	FileRoot        string        // root directory exposed to file transfer (default: home)
	Caps            []string      // allowed channel kinds (empty = all)
	CmdAllow        []string      // allowed exec/pty command basenames (empty = all)
	ReadOnly        bool          // forbid writes (exec, pty, forward, file put, sync push)
	Once            bool          // accept a single authenticated peer, then refuse
	MDNS            bool          // advertise on the local segment via mDNS
	Approve         bool          // prompt the operator to accept each new connection
	Identity        string        // path to a persistent host key (empty = ephemeral)
	STUN            string        // STUN server for a public reflexive candidate (optional)
	Rendezvous      string        // rendezvous broker base URL to register with (optional)
	Relay           string        // relay server address to register with (optional)
	Logger          Logger
}

// Host is the controllable side: it listens, authenticates dialers against the
// token's PSK, and serves concurrent isolated channels per connection.
type Host struct {
	id         *crypto.Identity
	ln         *transport.Listener
	tcp        *tcpmux.Listener
	tok        *token.Token
	encoded    string
	log        Logger
	caps       protocol.Capabilities
	limiter    *authLimiter
	fileRoot   string
	once       bool
	scope      scope
	approver   *approver
	authorized *identity.Authorized
	used       atomic.Bool
	opts       HostOptions
	extra      []func() // background registrations to stop on Close
}

// NewHost generates a fresh identity, starts a QUIC listener, enumerates
// candidate endpoints and mints the session token.
func NewHost(opts HostOptions) (*Host, error) {
	if opts.Logger == nil {
		opts.Logger = NopLogger{}
	}
	if opts.Bind == "" {
		opts.Bind = "0.0.0.0:0"
	}
	if opts.TTL <= 0 {
		opts.TTL = 24 * time.Hour
	}
	id, err := makeIdentity(opts.Identity)
	if err != nil {
		return nil, err
	}
	ln, err := transport.ListenQUIC(opts.Bind, id.ServerTLSConfig())
	if err != nil {
		return nil, err
	}
	port := uint16(ln.Port())
	candidates := discovery.EnumerateCandidates(port, opts.IncludeLoopback)
	if len(candidates) == 0 {
		// At minimum advertise loopback so the host is never unreachable.
		candidates = discovery.EnumerateCandidates(port, true)
	}
	// Best-effort public reflexive candidate (works for full-cone / port
	// preserving NATs). Symmetric NATs fall back to relay.
	if opts.STUN != "" {
		if refl := reflexiveCandidate(opts.STUN, port); refl != nil {
			candidates = append(candidates, *refl)
		}
	}
	tok, err := token.New(id.PublicKey(), candidates, opts.TTL)
	if err == nil && opts.Rendezvous != "" {
		tok.Rendezvous = opts.Rendezvous
	}
	if err != nil {
		ln.Close()
		return nil, err
	}
	encoded, err := tok.Encode()
	if err != nil {
		ln.Close()
		return nil, err
	}

	fileRoot := opts.FileRoot
	if fileRoot == "" {
		if home, herr := os.UserHomeDir(); herr == nil {
			fileRoot = home
		} else {
			fileRoot = "."
		}
	}

	h := &Host{
		id:       id,
		ln:       ln,
		tok:      tok,
		encoded:  encoded,
		log:      opts.Logger,
		caps:     LocalCapabilities(),
		limiter:  newAuthLimiter(),
		fileRoot: fileRoot,
		once:     opts.Once,
		scope:    newScope(opts.Caps, opts.CmdAllow, opts.ReadOnly),
		opts:     opts,
	}
	if opts.Approve {
		h.approver = newApprover()
	}
	// Load the enrolled-clients list so client-key auth works (empty if none).
	if auth, aerr := identity.LoadAuthorized(identity.DefaultAuthorizedPath()); aerr == nil {
		h.authorized = auth
	}

	// A TCP listener on the same port carries the fallback transports (plain
	// TCP, relay, SSH tunnel) which cannot ride QUIC's UDP. It presents the
	// same pinned identity, so authentication is identical to the QUIC path.
	host, _ := splitHostPort(opts.Bind)
	tcpAddr := fmt.Sprintf("%s:%d", host, port)
	if tcp, terr := tcpmux.ListenTCP(tcpAddr, id.ServerTLSConfig()); terr == nil {
		h.tcp = tcp
	} else {
		opts.Logger.Warning("remolo: TCP fallback listener not started: %v", terr)
	}

	return h, nil
}

// makeIdentity returns a persistent identity loaded from keyPath, or a fresh
// ephemeral one when keyPath is empty.
func makeIdentity(keyPath string) (*crypto.Identity, error) {
	if keyPath == "" {
		return crypto.GenerateIdentity()
	}
	priv, _, err := identity.LoadOrCreateHostKey(keyPath)
	if err != nil {
		return nil, err
	}
	return crypto.IdentityFromKey(priv)
}

func splitHostPort(bind string) (string, string) {
	for i := len(bind) - 1; i >= 0; i-- {
		if bind[i] == ':' {
			return bind[:i], bind[i+1:]
		}
	}
	return bind, ""
}

// Token returns the textual session token to share.
func (h *Host) Token() string { return h.encoded }

// Port returns the UDP port the host is listening on.
func (h *Host) Port() int { return h.ln.Port() }

// Candidates returns the advertised endpoints.
func (h *Host) Candidates() []token.Candidate { return h.tok.Candidates }

// Serve accepts connections until ctx is cancelled or the listener closes. It
// accepts on both the QUIC and the TCP-fallback listeners concurrently; both
// feed the same transport-agnostic handler.
func (h *Host) Serve(ctx context.Context) error {
	h.startServices(ctx)
	if h.tcp != nil {
		go h.serveTCP(ctx)
	}
	for {
		conn, err := h.ln.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		go h.handleConn(ctx, conn)
	}
}

func (h *Host) serveTCP(ctx context.Context) {
	for {
		conn, err := h.tcp.Accept(ctx)
		if err != nil {
			return
		}
		go h.handleConn(ctx, conn)
	}
}

// AddListener registers a background source of pre-authenticated transport
// connections (e.g. a relay registration) and a stopper to run on Close.
func (h *Host) AddListener(stop func()) { h.extra = append(h.extra, stop) }

// HandleConn feeds an externally-obtained connection (relay, custom transport)
// through the same authentication and channel-serving path.
func (h *Host) HandleConn(ctx context.Context, conn transport.Conn) { h.handleConn(ctx, conn) }

// Identity exposes the host identity for external listeners that must present
// the same pinned certificate (relay, SSH target).
func (h *Host) Identity() *crypto.Identity { return h.id }

// Close stops the listeners and any background registrations. It does not wipe
// the token PSK here: in-flight handshakes may still be reading it, and the
// process is tearing down anyway. (token.Zero remains available for callers that
// own a token exclusively.)
func (h *Host) Close() error {
	for _, stop := range h.extra {
		stop()
	}
	if h.tcp != nil {
		h.tcp.Close()
	}
	return h.ln.Close()
}

func (h *Host) handleConn(ctx context.Context, conn transport.Conn) {
	remote := conn.RemoteAddr().String()
	defer func() {
		if r := recover(); r != nil {
			h.log.Error("remolo: connection from %s panicked: %v", remote, r)
		}
	}()

	m := mux.New(conn)

	// The first channel must be the control channel; it carries the PSK
	// handshake and capability exchange before anything else is allowed.
	o, ctrl, err := m.Accept(ctx)
	if err != nil {
		conn.Close("no control channel")
		return
	}
	if o.Kind != protocol.KindControl {
		conn.Close("first channel must be control")
		return
	}

	if !h.limiter.allow(remote) {
		h.log.Warning("remolo: too many auth attempts from %s, throttling", remote)
		conn.Close("rate limited")
		return
	}
	// The first byte selects the authentication method: 'P' for the token PSK,
	// 'K' for an enrolled client key (no token needed).
	mode := make([]byte, 1)
	if _, err := ctrl.Read(mode); err != nil {
		conn.Close("no auth selector")
		return
	}
	switch mode[0] {
	case 'P':
		if _, err := crypto.ServerHandshake(ctrl, h.tok.PSK, h.id.PublicKey()); err != nil {
			h.log.Warning("remolo: authentication failed from %s: %v", remote, err)
			conn.Close("authentication failed")
			return
		}
	case 'K':
		if h.authorized == nil {
			conn.Close("client-key auth not enabled")
			return
		}
		cpub, err := identity.ServerAuth(ctrl, h.id.PrivateKey(), h.id.PublicKeyEd(), h.authorized.Contains)
		if err != nil {
			h.log.Warning("remolo: client-key auth failed from %s: %v", remote, err)
			conn.Close("authentication failed")
			return
		}
		h.log.Info("remolo: client key %s authenticated", identity.FingerprintBase64(cpub))
	default:
		conn.Close("unknown auth method")
		return
	}
	// Single-use tokens admit exactly one authenticated peer connection.
	if h.once && !h.used.CompareAndSwap(false, true) {
		h.log.Warning("remolo: single-use token already consumed, refusing %s", remote)
		conn.Close("single-use token already consumed")
		return
	}
	h.limiter.reset(remote)
	// Interactive consent: ask the operator before admitting the peer.
	if h.approver != nil && !h.approver.approve(remote) {
		h.log.Warning("remolo: connection from %s denied by operator", remote)
		conn.Close("connection declined")
		return
	}
	h.log.Info("remolo: peer authenticated from %s", remote)

	if err := protocol.WriteJSON(ctrl, h.caps); err != nil {
		conn.Close("capability send failed")
		return
	}

	// Per-connection state shared with channel handlers (SSH agent forwarding).
	st := &connState{ctx: ctx, mux: m, log: h.log}
	defer st.close()

	// Handle control-channel keepalives and post-handshake control messages.
	go h.serveControlState(ctrl, st)

	// Accept concurrent data channels; each is isolated so one crashing does
	// not affect the others.
	for {
		o, stream, err := m.Accept(ctx)
		if err != nil {
			return
		}
		go h.serveChannel(o, stream, st)
	}
}

func (h *Host) serveChannel(o protocol.Open, stream transport.Stream, st *connState) {
	defer func() {
		if r := recover(); r != nil {
			h.log.Error("remolo: channel %s panicked: %v", o.Kind, r)
		}
		stream.Close()
	}()

	// Audit: record every channel opened, with the command for exec/pty.
	if len(o.Command) > 0 {
		h.log.Info("audit: channel=%s command=%q", o.Kind, strings.Join(o.Command, " "))
	} else if o.Kind != protocol.KindControl {
		h.log.Info("audit: channel=%s target=%s", o.Kind, o.Target)
	}

	// Least-privilege enforcement: a scoped token may forbid whole channel kinds
	// (and write channels under read-only).
	if !h.scope.allowsKind(o.Kind) {
		h.log.Warning("remolo: channel %q denied by token scope", o.Kind)
		_ = protocol.WriteExit(stream, protocol.Exit{Code: -1, Err: fmt.Sprintf("channel %q not permitted by this token", o.Kind)})
		return
	}

	switch o.Kind {
	case protocol.KindPTY:
		h.servePTY(o, stream, st)
	case protocol.KindExec:
		h.serveExec(o, stream, st)
	case protocol.KindFile:
		if err := filetransfer.ServeWithPolicy(stream, h.fileRoot, !h.scope.readOnly); err != nil {
			h.log.Warning("remolo: file channel: %v", err)
		}
	case protocol.KindRPC:
		if err := hostRPCServer(h).Serve(stream); err != nil {
			h.log.Warning("remolo: rpc channel: %v", err)
		}
	case protocol.KindDesktop:
		h.serveDesktop(stream)
	case protocol.KindSync:
		if err := dirsync.ServeWithPolicy(stream, h.fileRoot, !h.scope.readOnly); err != nil {
			h.log.Warning("remolo: sync channel: %v", err)
		}
	case protocol.KindForward:
		if o.Target == "" {
			_ = protocol.WriteExit(stream, protocol.Exit{Code: -1, Err: "forward: empty target"})
			return
		}
		if err := tunnel.DialAndSplice(stream, o.Target); err != nil {
			h.log.Warning("remolo: forward to %s: %v", o.Target, err)
		}
	default:
		h.log.Warning("remolo: unsupported channel kind %q requested", o.Kind)
		_ = protocol.WriteExit(stream, protocol.Exit{Code: -1, Err: fmt.Sprintf("channel %q not supported by this host", o.Kind)})
	}
}

// serveDesktop wires real screen capture and input injection to the desktop
// channel. It uses the platform capturer (X11/Wayland/GDI/screencapture) when it
// works, and falls back to a synthetic source when no display is reachable or
// the capturer cannot produce a frame promptly (e.g. an unresponsive portal),
// so a session is never silently dead. Input injection is real on Linux.
func (h *Host) serveDesktop(stream transport.Stream) {
	cap := h.pickCapturer()
	var inj desktop.Injector = desktop.NopInjector{}
	if i, err := desktop.NewUinputInjector(); err == nil {
		inj = i
	}
	if err := desktop.Serve(stream, cap, inj); err != nil {
		h.log.Warning("remolo: desktop channel: %v", err)
	}
}

// pickCapturer returns a working capturer, probing the real one with a timeout
// so a hung backend cannot stall the whole session.
func (h *Host) pickCapturer() desktop.Capturer {
	const (
		synthW = 1280
		synthH = 720
	)
	c, err := desktop.NewCapturer()
	if err != nil {
		h.log.Info("remolo: desktop capture unavailable (%v), using synthetic source", err)
		return desktop.NewSyntheticCapturer(synthW, synthH)
	}
	// Probe: a real capturer must yield one frame within the timeout, otherwise
	// we treat it as unusable (a leaked probe goroutine is acceptable; the hung
	// backend would otherwise block forever).
	type res struct{ err error }
	done := make(chan res, 1)
	go func() {
		_, e := c.Capture()
		done <- res{e}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			h.log.Info("remolo: desktop capture probe failed (%v), using synthetic source", r.err)
			c.Close()
			return desktop.NewSyntheticCapturer(synthW, synthH)
		}
		return c
	case <-time.After(3 * time.Second):
		h.log.Info("remolo: desktop capture unresponsive, using synthetic source")
		return desktop.NewSyntheticCapturer(synthW, synthH)
	}
}
