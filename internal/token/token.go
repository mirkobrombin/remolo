// Package token implements remolo's self-describing connection token.
//
// A token is a compact, copy-paste-friendly blob that carries everything a
// client needs to find and authenticate an host: the host's public key, a
// pre-shared secret, every reachable endpoint (multi-subnet, IPv4 + IPv6),
// an optional rendezvous hint and an expiry. It is the pivot that ties
// discovery, transport and crypto together: paste the token and it connects.
//
// Wire format:
//
//	REMOLO1-XXXXXX-XXXXXX-...    (human prefix + dash-grouped Base32)
//
// The decoded payload is CBOR followed by a 4-byte CRC32 (Castagnoli)
// checksum, the whole thing Base32-Crockford encoded (case-insensitive,
// ambiguous characters folded on decode).
package token

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"net"
	"strings"
	"time"

	"github.com/fxamacker/cbor/v2"
)

// Version is the current token format version.
const Version uint8 = 1

// Prefix marks a remolo v1 token and provides at-a-glance recognisability.
const Prefix = "REMOLO1-"

const (
	keyLen     = 32 // ed25519 public key / PSK length
	sessionLen = 16 // peer/session identifier length
	groupSize  = 6  // characters per dash-separated group
)

// crockford is the Base32 alphabet defined by Douglas Crockford: it drops the
// ambiguous letters I, L, O and U. We decode without padding.
var crockford = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

var crcTable = crc32.MakeTable(crc32.Castagnoli)

// Candidate is a single reachable endpoint of the host.
type Candidate struct {
	IP   string `cbor:"1,keyasint"`
	Port uint16 `cbor:"2,keyasint"`
}

// Addr renders the candidate as a dial-able host:port string, bracketing IPv6.
func (c Candidate) Addr() string {
	return net.JoinHostPort(c.IP, fmt.Sprintf("%d", c.Port))
}

func (c Candidate) String() string { return c.Addr() }

// Token is the decoded connection blob.
type Token struct {
	Version    uint8       `cbor:"1,keyasint"`
	HostPubKey []byte      `cbor:"2,keyasint"`           // ed25519 public key (32 bytes)
	PSK        []byte      `cbor:"3,keyasint"`           // pre-shared secret (32 bytes)
	Candidates []Candidate `cbor:"4,keyasint"`           // every host endpoint
	SessionID  []byte      `cbor:"5,keyasint"`           // identifies the peer, not one session
	Expiry     int64       `cbor:"6,keyasint"`           // unix seconds
	Rendezvous string      `cbor:"7,keyasint,omitempty"` // optional broker hint
}

// New builds a token, generating a fresh PSK and session id. The host public
// key and candidate list are supplied by the caller. ttl sets the validity
// window from now.
func New(hostPubKey []byte, candidates []Candidate, ttl time.Duration) (*Token, error) {
	if len(hostPubKey) != keyLen {
		return nil, fmt.Errorf("token: host public key must be %d bytes, got %d", keyLen, len(hostPubKey))
	}
	psk := make([]byte, keyLen)
	if _, err := rand.Read(psk); err != nil {
		return nil, fmt.Errorf("token: generating psk: %w", err)
	}
	sid := make([]byte, sessionLen)
	if _, err := rand.Read(sid); err != nil {
		return nil, fmt.Errorf("token: generating session id: %w", err)
	}
	return &Token{
		Version:    Version,
		HostPubKey: append([]byte(nil), hostPubKey...),
		PSK:        psk,
		Candidates: candidates,
		SessionID:  sid,
		Expiry:     time.Now().Add(ttl).Unix(),
	}, nil
}

// Encode serialises the token to its textual form.
func (t *Token) Encode() (string, error) {
	if err := t.Validate(); err != nil {
		return "", err
	}
	payload, err := cbor.Marshal(t)
	if err != nil {
		return "", fmt.Errorf("token: cbor marshal: %w", err)
	}
	sum := crc32.Checksum(payload, crcTable)
	blob := make([]byte, len(payload)+4)
	copy(blob, payload)
	binary.BigEndian.PutUint32(blob[len(payload):], sum)

	encoded := crockford.EncodeToString(blob)
	return Prefix + group(encoded), nil
}

// Decode parses a textual token, verifies its checksum and validates structure.
func Decode(s string) (*Token, error) {
	raw := normalize(s)
	if raw == "" {
		return nil, fmt.Errorf("token: empty")
	}
	blob, err := crockford.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("token: bad encoding (check for typos): %w", err)
	}
	if len(blob) < 5 {
		return nil, fmt.Errorf("token: too short")
	}
	payload := blob[:len(blob)-4]
	want := binary.BigEndian.Uint32(blob[len(blob)-4:])
	if got := crc32.Checksum(payload, crcTable); got != want {
		return nil, fmt.Errorf("token: checksum mismatch (token copied incompletely?)")
	}
	var t Token
	if err := cbor.Unmarshal(payload, &t); err != nil {
		return nil, fmt.Errorf("token: cbor unmarshal: %w", err)
	}
	if err := t.Validate(); err != nil {
		return nil, err
	}
	return &t, nil
}

// Validate checks structural invariants. It does not check expiry; use Expired.
func (t *Token) Validate() error {
	if t.Version != Version {
		return fmt.Errorf("token: unsupported version %d (this build speaks v%d)", t.Version, Version)
	}
	if len(t.HostPubKey) != keyLen {
		return fmt.Errorf("token: host public key must be %d bytes", keyLen)
	}
	if len(t.PSK) != keyLen {
		return fmt.Errorf("token: psk must be %d bytes", keyLen)
	}
	if len(t.SessionID) != sessionLen {
		return fmt.Errorf("token: session id must be %d bytes", sessionLen)
	}
	if len(t.Candidates) == 0 && t.Rendezvous == "" {
		return fmt.Errorf("token: no candidates and no rendezvous hint, host is unreachable")
	}
	for i, c := range t.Candidates {
		if c.Port == 0 {
			return fmt.Errorf("token: candidate %d has port 0", i)
		}
		if net.ParseIP(c.IP) == nil {
			return fmt.Errorf("token: candidate %d has invalid ip %q", i, c.IP)
		}
	}
	return nil
}

// Zero wipes the token's secret material (PSK) from memory. Call it once the
// token is no longer needed, to shrink the window in which the credential is
// recoverable from a memory dump.
func (t *Token) Zero() {
	for i := range t.PSK {
		t.PSK[i] = 0
	}
}

// Expired reports whether the token's validity window has passed.
func (t *Token) Expired() bool { return time.Now().Unix() > t.Expiry }

// ExpiresIn returns the remaining validity duration (negative if expired).
func (t *Token) ExpiresIn() time.Duration {
	return time.Until(time.Unix(t.Expiry, 0))
}

// normalize folds a pasted token back to a clean Base32 string: it strips the
// prefix and separators, upper-cases, and maps Crockford's ambiguous glyphs
// (O to 0, I and L to 1) so that human transcription errors still decode.
func normalize(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	// Strip the literal version tag. We trim it before glyph folding because
	// the tag itself contains O and L, which folding would otherwise mangle.
	// We tolerate the trailing dash being missing or spaced out.
	const tag = "REMOLO1"
	if strings.HasPrefix(s, tag) {
		s = s[len(tag):]
	}
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '-', ' ', '\t', '\n', '\r':
			continue
		case 'O':
			b.WriteByte('0')
		case 'I', 'L':
			b.WriteByte('1')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// group inserts dashes every groupSize characters for readable copy-paste.
func group(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i += groupSize {
		if i > 0 {
			b.WriteByte('-')
		}
		end := i + groupSize
		if end > len(s) {
			end = len(s)
		}
		b.WriteString(s[i:end])
	}
	return b.String()
}
