package identity

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// DefaultAuthorizedPath returns the default location of the host's
// authorized-clients list (~/.config/remolo/authorized_clients).
func DefaultAuthorizedPath() string { return configPath("authorized_clients") }

// AuthorizedEntry is one client public key allowed in without a token, plus an
// optional human-readable label.
type AuthorizedEntry struct {
	Pub   ed25519.PublicKey
	Label string
}

// Authorized is the host-side list of client public keys permitted to
// authenticate with their own key instead of a one-shot token. It is safe for
// concurrent use.
//
// On-disk format: one entry per line, "base64pubkey[ label...]". Blank lines
// and lines beginning with '#' are ignored. A trailing inline '#' comment on an
// entry line is also stripped.
type Authorized struct {
	mu   sync.Mutex
	path string
	// entries preserves insertion/file order; keyed lookups scan it.
	entries []AuthorizedEntry
}

// LoadAuthorized reads the authorized-clients list at path. A missing file is
// treated as an empty (but writable) list.
func LoadAuthorized(path string) (*Authorized, error) {
	a := &Authorized{path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return a, nil
		}
		return nil, fmt.Errorf("identity: read authorized clients %q: %w", path, err)
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	lineNo := 0
	for sc.Scan() {
		lineNo++
		entry, ok, err := parseAuthorizedLine(sc.Text())
		if err != nil {
			return nil, fmt.Errorf("identity: %q line %d: %w", path, lineNo, err)
		}
		if ok {
			a.entries = append(a.entries, entry)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("identity: scan authorized clients %q: %w", path, err)
	}
	return a, nil
}

// parseAuthorizedLine parses one line. ok is false for blank/comment lines.
func parseAuthorizedLine(line string) (AuthorizedEntry, bool, error) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return AuthorizedEntry{}, false, nil
	}
	// Split off the base64 token; the remainder is the label, with an optional
	// trailing inline comment removed.
	fields := strings.Fields(trimmed)
	pub, err := decodePub(fields[0])
	if err != nil {
		return AuthorizedEntry{}, false, err
	}
	label := ""
	if len(fields) > 1 {
		rest := strings.TrimSpace(strings.TrimPrefix(trimmed, fields[0]))
		if i := strings.Index(rest, "#"); i >= 0 {
			rest = strings.TrimSpace(rest[:i])
		}
		label = rest
	}
	return AuthorizedEntry{Pub: pub, Label: label}, true, nil
}

// Contains reports whether pub is on the authorized list.
func (a *Authorized) Contains(pub ed25519.PublicKey) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.indexLocked(pub) >= 0
}

// indexLocked returns the index of pub in entries, or -1. Caller holds mu.
func (a *Authorized) indexLocked(pub ed25519.PublicKey) int {
	for i := range a.entries {
		if a.entries[i].Pub.Equal(pub) {
			return i
		}
	}
	return -1
}

// List returns a copy of the current entries in file order.
func (a *Authorized) List() []AuthorizedEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]AuthorizedEntry, len(a.entries))
	for i, e := range a.entries {
		out[i] = AuthorizedEntry{Pub: append(ed25519.PublicKey(nil), e.Pub...), Label: e.Label}
	}
	return out
}

// Add authorizes pub with an optional label and persists the list. Adding a key
// that is already present updates its label.
func (a *Authorized) Add(pub ed25519.PublicKey, label string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if i := a.indexLocked(pub); i >= 0 {
		a.entries[i].Label = label
	} else {
		a.entries = append(a.entries, AuthorizedEntry{
			Pub:   append(ed25519.PublicKey(nil), pub...),
			Label: label,
		})
	}
	return a.persistLocked()
}

// Remove drops pub from the list and persists it. It reports whether an entry
// was actually removed.
func (a *Authorized) Remove(pub ed25519.PublicKey) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	i := a.indexLocked(pub)
	if i < 0 {
		return false
	}
	a.entries = append(a.entries[:i], a.entries[i+1:]...)
	// Best effort persist; the in-memory removal still holds for this process.
	_ = a.persistLocked()
	return true
}

// persistLocked writes the list atomically. Caller holds mu.
func (a *Authorized) persistLocked() error {
	var buf bytes.Buffer
	buf.WriteString("# remolo authorized clients: one base64 Ed25519 public key per line\n")
	for _, e := range a.entries {
		buf.WriteString(encodePub(e.Pub))
		if e.Label != "" {
			buf.WriteByte(' ')
			buf.WriteString(e.Label)
		}
		buf.WriteByte('\n')
	}
	return writeFileAtomic(a.path, buf.Bytes(), keyFileMode)
}

// writeFileAtomic writes data to path via a temp file and rename, creating the
// parent directory if needed.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, dirMode); err != nil {
			return fmt.Errorf("identity: create directory %q: %w", dir, err)
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("identity: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return fmt.Errorf("identity: chmod temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("identity: write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("identity: close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("identity: rename temp file: %w", err)
	}
	return nil
}
