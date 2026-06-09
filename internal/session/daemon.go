package session

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/mirkobrombin/remolo/internal/protocol"
	"github.com/mirkobrombin/remolo/internal/transport"
)

// The mux daemon lets a single authenticated connection back many sessions.
// The first `remolo connect` owns the connection and runs a daemon on a local
// unix socket keyed by the peer's session id. Later invocations (a desktop, a
// second shell, an exec) detect that socket and ask the daemon to open another
// logical channel on the existing connection, avoiding a second NAT traversal
// and handshake. The socket transparently proxies the channel's frames, so the
// caller drives it exactly as if it held the remote stream directly.

// SessionInfo is the metadata advertised by a running daemon, for `remolo
// sessions`.
type SessionInfo struct {
	SessionID string    `json:"session_id"`
	Hostname  string    `json:"hostname"`
	Route     string    `json:"route"`
	OS        string    `json:"os"`
	PID       int       `json:"pid"`
	Started   time.Time `json:"started"`
	Socket    string    `json:"-"`
}

// maxSockPath is a conservative cap below the AF_UNIX sun_path limit (108 on
// Linux, 104 on macOS), leaving room for the "/<32 hex>.sock" filename.
const maxSockPath = 100

// runtimeDir picks a per-user directory for the daemon's control socket. It
// prefers XDG_RUNTIME_DIR, but falls back to a short temp path if that would
// push the socket path past the kernel's sun_path length limit.
func runtimeDir() string {
	tmpBase := filepath.Join(os.TempDir(), fmt.Sprintf("remolo-%d", os.Getuid()))
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		base := filepath.Join(d, "remolo")
		// "/" + 32 hex chars + ".sock" == 38 chars of filename.
		if len(base)+38 <= maxSockPath {
			return base
		}
	}
	return tmpBase
}

func socketPath(sessionID []byte) string {
	return filepath.Join(runtimeDir(), hex.EncodeToString(sessionID)+".sock")
}

func metaPath(sessionID []byte) string {
	return filepath.Join(runtimeDir(), hex.EncodeToString(sessionID)+".json")
}

// Daemon owns a Client connection and serves channel-open requests locally.
type Daemon struct {
	cl        *Client
	ln        net.Listener
	sessionID []byte
	log       Logger
	sockPath  string
	metaFile  string
}

// StartDaemon binds the local control socket for a session and starts serving
// channel-open requests against the given client.
func StartDaemon(cl *Client, sessionID []byte, log Logger) (*Daemon, error) {
	if log == nil {
		log = NopLogger{}
	}
	if err := os.MkdirAll(runtimeDir(), 0o700); err != nil {
		return nil, fmt.Errorf("daemon: create runtime dir: %w", err)
	}
	sock := socketPath(sessionID)
	// Remove a stale socket from a crashed predecessor.
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return nil, fmt.Errorf("daemon: listen: %w", err)
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		ln.Close()
		return nil, err
	}

	d := &Daemon{cl: cl, ln: ln, sessionID: sessionID, log: log, sockPath: sock, metaFile: metaPath(sessionID)}
	if err := d.writeMeta(); err != nil {
		ln.Close()
		os.Remove(sock)
		return nil, err
	}
	go d.serve()
	return d, nil
}

func (d *Daemon) writeMeta() error {
	caps := d.cl.Capabilities()
	info := SessionInfo{
		SessionID: hex.EncodeToString(d.sessionID),
		Hostname:  caps.Hostname,
		Route:     d.cl.Route(),
		OS:        caps.OS,
		PID:       os.Getpid(),
		Started:   time.Now(),
	}
	b, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(d.metaFile, b, 0o600)
}

func (d *Daemon) serve() {
	for {
		conn, err := d.ln.Accept()
		if err != nil {
			return
		}
		go d.handleLocal(conn)
	}
}

// handleLocal reads the requested channel descriptor from a local caller, opens
// the corresponding remote channel on the shared connection, and splices the
// two together so the caller's frames flow to the host and back.
func (d *Daemon) handleLocal(local net.Conn) {
	defer local.Close()
	open, err := protocol.ReadOpen(local)
	if err != nil {
		d.log.Warning("remolo daemon: bad channel request: %v", err)
		return
	}
	remote, err := d.cl.OpenChannel(context.Background(), open)
	if err != nil {
		d.log.Warning("remolo daemon: open remote channel: %v", err)
		return
	}
	proxy(local, remote)
}

// proxy splices a local connection and a remote stream until either side ends.
func proxy(local net.Conn, remote transport.Stream) {
	done := make(chan struct{}, 2)
	go func() { io.Copy(remote, local); remote.Close(); done <- struct{}{} }()
	go func() { io.Copy(local, remote); local.Close(); done <- struct{}{} }()
	<-done
}

// Close stops the daemon and removes its socket and metadata.
func (d *Daemon) Close() error {
	err := d.ln.Close()
	os.Remove(d.sockPath)
	os.Remove(d.metaFile)
	return err
}

// Client exposes the owned client (used by the primary process to open its own
// first channel directly).
func (d *Daemon) Client() *Client { return d.cl }
