package session

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mirkobrombin/remolo/internal/protocol"
	"github.com/mirkobrombin/remolo/internal/transport"
)

// DaemonChannel is a channel opened through a running mux daemon. It satisfies
// transport.Stream, so the caller bridges it exactly like a direct channel.
type DaemonChannel struct {
	net.Conn
}

var _ transport.Stream = (*DaemonChannel)(nil)

// DialDaemon connects to an existing mux daemon for the given session id and
// requests a channel described by open. It returns the channel and true on
// success, or (nil, false) if no live daemon is present.
func DialDaemon(sessionID []byte, open protocol.Open) (*DaemonChannel, bool, error) {
	sock := socketPath(sessionID)
	if _, err := os.Stat(sock); err != nil {
		return nil, false, nil
	}
	conn, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		// Stale socket from a crashed owner: clean it up.
		_ = os.Remove(sock)
		return nil, false, nil
	}
	if err := protocol.WriteOpen(conn, open); err != nil {
		conn.Close()
		return nil, false, err
	}
	return &DaemonChannel{Conn: conn}, true, nil
}

// HasDaemon reports whether a live daemon owns the session.
func HasDaemon(sessionID []byte) bool {
	sock := socketPath(sessionID)
	if _, err := os.Stat(sock); err != nil {
		return false
	}
	conn, err := net.DialTimeout("unix", sock, time.Second)
	if err != nil {
		_ = os.Remove(sock)
		return false
	}
	conn.Close()
	return true
}

// CloseDaemon terminates the mux daemon whose session id starts with idPrefix
// (the owning process) and removes its socket and metadata. It returns the
// closed session's metadata.
func CloseDaemon(idPrefix string) (SessionInfo, error) {
	sessions, err := ListSessions()
	if err != nil {
		return SessionInfo{}, err
	}
	for _, s := range sessions {
		if !strings.HasPrefix(s.SessionID, idPrefix) {
			continue
		}
		if p, perr := os.FindProcess(s.PID); perr == nil {
			_ = p.Kill()
		}
		_ = os.Remove(s.Socket)
		_ = os.Remove(strings.TrimSuffix(s.Socket, ".sock") + ".json")
		return s, nil
	}
	return SessionInfo{}, fmt.Errorf("no active session matching %q", idPrefix)
}

// ListSessions returns metadata for every live daemon, pruning stale entries.
func ListSessions() ([]SessionInfo, error) {
	dir := runtimeDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []SessionInfo
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var info SessionInfo
		if json.Unmarshal(b, &info) != nil {
			continue
		}
		sock := filepath.Join(dir, strings.TrimSuffix(e.Name(), ".json")+".sock")
		info.Socket = sock
		// Liveness check: a daemon must answer on its socket.
		conn, derr := net.DialTimeout("unix", sock, time.Second)
		if derr != nil {
			// Owner is gone: prune both files.
			_ = os.Remove(sock)
			_ = os.Remove(filepath.Join(dir, e.Name()))
			continue
		}
		conn.Close()
		out = append(out, info)
	}
	return out, nil
}
