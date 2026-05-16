// Package identity adds an SSH-like persistent-trust model to remolo,
// alongside the existing ephemeral, token-anchored handshake in
// internal/crypto.
//
// Three pieces cooperate:
//
//  1. Persistent keys. A host keeps a STABLE Ed25519 key on disk; a client
//     keeps its own keypair. Keys are stored PEM-encoded (PKCS8) with 0600
//     permissions and loaded on every start (see keys.go).
//
//  2. Trust lists. The host keeps an authorized-clients list (public keys it
//     lets in without a token, see authorized.go). The client keeps a
//     known-hosts memory of which host key it saw for a given alias, so it can
//     refuse a swapped host key on a later connection (see knownhosts.go).
//
//  3. Client-key mutual auth. A signature-based challenge-response that proves
//     the client holds an authorized private key and re-confirms the host key,
//     analogous to the PSK handshake but asymmetric (see auth.go).
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
)

const (
	// pemType is the PEM block type used for stored private keys.
	pemType = "PRIVATE KEY"
	// keyFileMode is the permission bits for stored private key files.
	keyFileMode os.FileMode = 0o600
	// dirMode is the permission bits for the config directory.
	dirMode os.FileMode = 0o700
)

// configDir returns the remolo config directory, honoring $XDG_CONFIG_HOME and
// falling back to ~/.config when it is unset.
func configDir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "remolo"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("identity: locate home directory: %w", err)
	}
	return filepath.Join(home, ".config", "remolo"), nil
}

// configPath joins name onto the remolo config directory.
func configPath(name string) string {
	dir, err := configDir()
	if err != nil {
		// Best effort: fall back to a relative path under the cwd. The caller
		// can still override with an explicit path.
		return filepath.Join(".remolo", name)
	}
	return filepath.Join(dir, name)
}

// DefaultHostKeyPath returns the default location of the persistent host key
// (~/.config/remolo/host.key, honoring $XDG_CONFIG_HOME).
func DefaultHostKeyPath() string { return configPath("host.key") }

// DefaultClientKeyPath returns the default location of the persistent client
// key (~/.config/remolo/client.key, honoring $XDG_CONFIG_HOME).
func DefaultClientKeyPath() string { return configPath("client.key") }

// LoadOrCreateHostKey loads the host's persistent Ed25519 key from path, or
// creates and persists a fresh one (0600, parent dir created) if it is absent.
func LoadOrCreateHostKey(path string) (ed25519.PrivateKey, ed25519.PublicKey, error) {
	return loadOrCreateKey(path)
}

// LoadOrCreateClientKey loads the client's persistent Ed25519 key from path, or
// creates and persists a fresh one (0600, parent dir created) if it is absent.
// It shares its implementation with LoadOrCreateHostKey; the two names exist
// only to make call sites read clearly.
func LoadOrCreateClientKey(path string) (ed25519.PrivateKey, ed25519.PublicKey, error) {
	return loadOrCreateKey(path)
}

func loadOrCreateKey(path string) (ed25519.PrivateKey, ed25519.PublicKey, error) {
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		return parsePrivateKey(data)
	case os.IsNotExist(err):
		return createKey(path)
	default:
		return nil, nil, fmt.Errorf("identity: read key %q: %w", path, err)
	}
}

func createKey(path string) (ed25519.PrivateKey, ed25519.PublicKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("identity: generate key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, nil, fmt.Errorf("identity: marshal key: %w", err)
	}
	block := &pem.Block{Type: pemType, Bytes: der}
	encoded := pem.EncodeToMemory(block)
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, dirMode); err != nil {
			return nil, nil, fmt.Errorf("identity: create key directory %q: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, encoded, keyFileMode); err != nil {
		return nil, nil, fmt.Errorf("identity: write key %q: %w", path, err)
	}
	return priv, pub, nil
}

func parsePrivateKey(data []byte) (ed25519.PrivateKey, ed25519.PublicKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, nil, fmt.Errorf("identity: no PEM block found in key file")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("identity: parse key: %w", err)
	}
	priv, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, nil, fmt.Errorf("identity: key is not Ed25519 (got %T)", key)
	}
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return nil, nil, fmt.Errorf("identity: cannot derive public key")
	}
	return priv, pub, nil
}

// FingerprintBase64 returns a stable, displayable fingerprint of a public key:
// the standard base64 encoding of its SHA-256 digest.
func FingerprintBase64(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// encodePub returns the base64 (standard) encoding of a raw public key, the
// form used in the on-disk trust lists.
func encodePub(pub ed25519.PublicKey) string {
	return base64.StdEncoding.EncodeToString(pub)
}

// decodePub parses a base64 (standard) encoded Ed25519 public key.
func decodePub(s string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("identity: decode public key: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("identity: public key has wrong length %d", len(raw))
	}
	return ed25519.PublicKey(raw), nil
}
