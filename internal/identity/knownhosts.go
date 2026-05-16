package identity

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"fmt"
	"os"
	"strings"
	"sync"
)

// DefaultKnownHostsPath returns the default location of the client's
// known-hosts memory (~/.config/remolo/known_hosts).
func DefaultKnownHostsPath() string { return configPath("known_hosts") }

// Verdict is the result of checking a host key against known-hosts memory.
type Verdict int

const (
	// TrustNew means the id has never been seen; trust-on-first-use applies.
	TrustNew Verdict = iota
	// TrustMatch means the id was seen before with the SAME key.
	TrustMatch
	// TrustMismatch means the id was seen before with a DIFFERENT key. This is
	// the dangerous anti-swap case and the connection should be refused.
	TrustMismatch
)

// String renders a Verdict for logs and errors.
func (v Verdict) String() string {
	switch v {
	case TrustNew:
		return "new"
	case TrustMatch:
		return "match"
	case TrustMismatch:
		return "mismatch"
	default:
		return "unknown"
	}
}

// KnownHosts is the client-side memory of which host key was seen for a given
// id (an alias or session identifier). It is safe for concurrent use.
//
// On-disk format: one entry per line, "id base64pubkey". Blank lines and lines
// beginning with '#' are ignored.
type KnownHosts struct {
	mu    sync.Mutex
	path  string
	hosts map[string]ed25519.PublicKey
}

// LoadKnownHosts reads the known-hosts file at path. A missing file is treated
// as an empty (but writable) memory.
func LoadKnownHosts(path string) (*KnownHosts, error) {
	k := &KnownHosts{path: path, hosts: make(map[string]ed25519.PublicKey)}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return k, nil
		}
		return nil, fmt.Errorf("identity: read known hosts %q: %w", path, err)
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return nil, fmt.Errorf("identity: %q line %d: expected \"id base64pubkey\"", path, lineNo)
		}
		pub, err := decodePub(fields[1])
		if err != nil {
			return nil, fmt.Errorf("identity: %q line %d: %w", path, lineNo, err)
		}
		k.hosts[fields[0]] = pub
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("identity: scan known hosts %q: %w", path, err)
	}
	return k, nil
}

// Check compares pub against the key remembered for id and returns a Verdict.
// It does not mutate memory; call Remember to record a TrustNew host.
func (k *KnownHosts) Check(id string, pub ed25519.PublicKey) Verdict {
	k.mu.Lock()
	defer k.mu.Unlock()
	seen, ok := k.hosts[id]
	if !ok {
		return TrustNew
	}
	if seen.Equal(pub) {
		return TrustMatch
	}
	return TrustMismatch
}

// Remember records pub as the trusted host key for id and persists the memory.
// It overwrites any previous key for id, so callers should only Remember after
// resolving a TrustNew (or a deliberately re-accepted) host.
func (k *KnownHosts) Remember(id string, pub ed25519.PublicKey) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.hosts[id] = append(ed25519.PublicKey(nil), pub...)
	return k.persistLocked()
}

// persistLocked writes the known-hosts file atomically. Caller holds mu.
func (k *KnownHosts) persistLocked() error {
	var buf bytes.Buffer
	buf.WriteString("# remolo known hosts: \"id base64pubkey\" per line\n")
	for id, pub := range k.hosts {
		buf.WriteString(id)
		buf.WriteByte(' ')
		buf.WriteString(encodePub(pub))
		buf.WriteByte('\n')
	}
	return writeFileAtomic(k.path, buf.Bytes(), keyFileMode)
}
