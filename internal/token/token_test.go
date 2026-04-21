package token

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"
)

func mustKey(t *testing.T) []byte {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	return pub
}

func sampleCandidates() []Candidate {
	return []Candidate{
		{IP: "192.168.1.10", Port: 41000},
		{IP: "10.0.0.5", Port: 41000},
		{IP: "fe80::1", Port: 41000},
	}
}

func TestRoundTrip(t *testing.T) {
	pub := mustKey(t)
	tok, err := New(pub, sampleCandidates(), time.Hour)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	enc, err := tok.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !strings.HasPrefix(enc, Prefix) {
		t.Fatalf("missing prefix: %q", enc)
	}
	dec, err := Decode(enc)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if string(dec.HostPubKey) != string(pub) {
		t.Errorf("pubkey mismatch")
	}
	if string(dec.PSK) != string(tok.PSK) {
		t.Errorf("psk mismatch")
	}
	if string(dec.SessionID) != string(tok.SessionID) {
		t.Errorf("session id mismatch")
	}
	if len(dec.Candidates) != 3 {
		t.Fatalf("want 3 candidates, got %d", len(dec.Candidates))
	}
	if dec.Candidates[0].Addr() != "192.168.1.10:41000" {
		t.Errorf("bad addr: %q", dec.Candidates[0].Addr())
	}
}

func TestCaseAndSeparatorInsensitive(t *testing.T) {
	tok, _ := New(mustKey(t), sampleCandidates(), time.Hour)
	enc, _ := tok.Encode()
	body := strings.TrimPrefix(enc, Prefix)

	// Lower-case body, extra spaces, line breaks and stray separators in the
	// payload must still decode (the literal prefix is left intact).
	mangled := "  " + Prefix + strings.ToLower(strings.ReplaceAll(body, "-", " - ")) + "\n"
	if _, err := Decode(mangled); err != nil {
		t.Fatalf("expected tolerant decode, got: %v", err)
	}
}

func TestAmbiguousGlyphFolding(t *testing.T) {
	tok, _ := New(mustKey(t), sampleCandidates(), time.Hour)
	enc, _ := tok.Encode()
	body := strings.TrimPrefix(enc, Prefix)
	// A user typing O for 0 and l for 1 in the body should still land a valid
	// token, since Crockford folds these on decode (the prefix is left intact).
	folded := Prefix + strings.NewReplacer("0", "O", "1", "l").Replace(body)
	if _, err := Decode(folded); err != nil {
		t.Fatalf("glyph folding failed: %v", err)
	}
}

func TestChecksumCatchesCorruption(t *testing.T) {
	tok, _ := New(mustKey(t), sampleCandidates(), time.Hour)
	enc, _ := tok.Encode()
	body := strings.TrimPrefix(enc, Prefix)
	body = strings.ReplaceAll(body, "-", "")
	// Flip one character in the payload region (not the very end).
	chars := []byte(body)
	idx := len(chars) / 3
	if chars[idx] == 'A' {
		chars[idx] = 'B'
	} else {
		chars[idx] = 'A'
	}
	if _, err := Decode(Prefix + string(chars)); err == nil {
		t.Fatal("expected checksum error on corrupted token")
	}
}

func TestExpiry(t *testing.T) {
	tok, _ := New(mustKey(t), sampleCandidates(), -time.Minute)
	if !tok.Expired() {
		t.Fatal("token with negative ttl should be expired")
	}
	if tok.ExpiresIn() > 0 {
		t.Fatal("ExpiresIn should be negative for expired token")
	}
}

func TestValidateRejectsBad(t *testing.T) {
	cases := map[string]*Token{
		"bad version":  {Version: 99, HostPubKey: make([]byte, 32), PSK: make([]byte, 32), SessionID: make([]byte, 16), Candidates: sampleCandidates()},
		"short pubkey": {Version: 1, HostPubKey: make([]byte, 5), PSK: make([]byte, 32), SessionID: make([]byte, 16), Candidates: sampleCandidates()},
		"no endpoints": {Version: 1, HostPubKey: make([]byte, 32), PSK: make([]byte, 32), SessionID: make([]byte, 16)},
		"bad ip":       {Version: 1, HostPubKey: make([]byte, 32), PSK: make([]byte, 32), SessionID: make([]byte, 16), Candidates: []Candidate{{IP: "not-an-ip", Port: 1}}},
		"zero port":    {Version: 1, HostPubKey: make([]byte, 32), PSK: make([]byte, 32), SessionID: make([]byte, 16), Candidates: []Candidate{{IP: "1.2.3.4", Port: 0}}},
	}
	for name, tok := range cases {
		if err := tok.Validate(); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func TestDecodeGarbageDoesNotPanic(t *testing.T) {
	for _, s := range []string{"", "REMOLO1-", "REMOLO1-!!!!", "hello world", Prefix + strings.Repeat("A", 9)} {
		if _, err := Decode(s); err == nil {
			t.Errorf("expected error for %q", s)
		}
	}
}

// FuzzDecode ensures the token parser never panics on arbitrary input, since
// it is an attacker-reachable surface.
func FuzzDecode(f *testing.F) {
	tok, _ := New(mustKey(&testing.T{}), sampleCandidates(), time.Hour)
	if enc, err := tok.Encode(); err == nil {
		f.Add(enc)
	}
	f.Add("REMOLO1-AAAAAA-BBBBBB")
	f.Add("")
	f.Add("REMOLO1-")
	f.Fuzz(func(t *testing.T, s string) {
		_, _ = Decode(s) // must not panic
	})
}
