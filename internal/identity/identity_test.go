package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func genKey(t *testing.T) (ed25519.PrivateKey, ed25519.PublicKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return priv, pub
}

func TestLoadOrCreateKeyPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "host.key")

	priv1, pub1, err := LoadOrCreateHostKey(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("key perms = %o, want 600", perm)
	}

	priv2, pub2, err := LoadOrCreateHostKey(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !priv1.Equal(priv2) || !pub1.Equal(pub2) {
		t.Fatal("reloaded key differs from created key")
	}
	// Client variant shares the implementation.
	if _, _, err := LoadOrCreateClientKey(filepath.Join(dir, "client.key")); err != nil {
		t.Fatalf("client key: %v", err)
	}
}

func TestFingerprintBase64Stable(t *testing.T) {
	_, pub := genKey(t)
	if FingerprintBase64(pub) != FingerprintBase64(pub) {
		t.Fatal("fingerprint not stable")
	}
	_, other := genKey(t)
	if FingerprintBase64(pub) == FingerprintBase64(other) {
		t.Fatal("distinct keys share a fingerprint")
	}
}

func TestDefaultPathsHonorXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	if got := DefaultHostKeyPath(); got != "/xdg/remolo/host.key" {
		t.Fatalf("host key path = %q", got)
	}
	if got := DefaultClientKeyPath(); got != "/xdg/remolo/client.key" {
		t.Fatalf("client key path = %q", got)
	}
	if got := DefaultAuthorizedPath(); got != "/xdg/remolo/authorized_clients" {
		t.Fatalf("authorized path = %q", got)
	}
	if got := DefaultKnownHostsPath(); got != "/xdg/remolo/known_hosts" {
		t.Fatalf("known hosts path = %q", got)
	}
}

func TestAuthorizedAddContainsRemovePersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "authorized_clients")
	a, err := LoadAuthorized(path)
	if err != nil {
		t.Fatalf("load empty: %v", err)
	}
	if len(a.List()) != 0 {
		t.Fatal("missing file should yield empty list")
	}

	_, pub := genKey(t)
	if a.Contains(pub) {
		t.Fatal("unexpected membership")
	}
	if err := a.Add(pub, "laptop"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if !a.Contains(pub) {
		t.Fatal("expected membership after add")
	}

	// Reload from disk to confirm persistence including the label.
	a2, err := LoadAuthorized(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !a2.Contains(pub) {
		t.Fatal("persisted entry not found after reload")
	}
	entries := a2.List()
	if len(entries) != 1 || entries[0].Label != "laptop" {
		t.Fatalf("unexpected entries after reload: %+v", entries)
	}

	if !a2.Remove(pub) {
		t.Fatal("remove should report true")
	}
	if a2.Remove(pub) {
		t.Fatal("second remove should report false")
	}
	a3, _ := LoadAuthorized(path)
	if a3.Contains(pub) {
		t.Fatal("entry should be gone after persisted remove")
	}
}

func TestAuthorizedParsingComments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "authorized_clients")
	_, pub := genKey(t)
	content := "# a header comment\n\n" + encodePub(pub) + " my label # trailing\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := LoadAuthorized(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	entries := a.List()
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	if entries[0].Label != "my label" {
		t.Fatalf("label = %q, want %q", entries[0].Label, "my label")
	}
	if !a.Contains(pub) {
		t.Fatal("parsed key not found")
	}
}

func TestKnownHostsVerdicts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	k, err := LoadKnownHosts(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	_, pub := genKey(t)

	if v := k.Check("alias-a", pub); v != TrustNew {
		t.Fatalf("first sight = %v, want TrustNew", v)
	}
	if err := k.Remember("alias-a", pub); err != nil {
		t.Fatalf("remember: %v", err)
	}
	if v := k.Check("alias-a", pub); v != TrustMatch {
		t.Fatalf("same key = %v, want TrustMatch", v)
	}
	_, other := genKey(t)
	if v := k.Check("alias-a", other); v != TrustMismatch {
		t.Fatalf("swapped key = %v, want TrustMismatch", v)
	}

	// Persistence across reload.
	k2, err := LoadKnownHosts(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if v := k2.Check("alias-a", pub); v != TrustMatch {
		t.Fatalf("reloaded match = %v, want TrustMatch", v)
	}
	if v := k2.Check("alias-a", other); v != TrustMismatch {
		t.Fatalf("reloaded mismatch = %v, want TrustMismatch", v)
	}
}

// runAuth drives both sides over an in-memory pipe, mirroring the crypto
// package's runHandshake: it closes the client end afterward so a server still
// blocked on a frame that will never arrive unblocks instead of deadlocking.
func runAuth(clientPriv ed25519.PrivateKey, clientPub, clientHostPub ed25519.PublicKey,
	hostPriv ed25519.PrivateKey, hostPub ed25519.PublicKey,
	isAuthorized func(ed25519.PublicKey) bool,
	tamper func([]byte)) (gotClient ed25519.PublicKey, cErr, sErr error) {

	c, s := net.Pipe()
	done := make(chan struct{})
	go func() {
		gotClient, sErr = ServerAuth(s, hostPriv, hostPub, isAuthorized)
		// Close the server end so a client blocked on a synchronous net.Pipe
		// write (the server rejected early and stopped reading) unblocks with
		// an error rather than deadlocking.
		s.Close()
		close(done)
	}()
	// Also bound the client with a deadline as a backstop.
	c.SetDeadline(time.Now().Add(10 * time.Second))
	var crw io.ReadWriter = c
	if tamper != nil {
		crw = &tamperingRW{ReadWriter: c, mutate: tamper}
	}
	cErr = ClientAuth(crw, clientPriv, clientPub, clientHostPub)
	c.Close()
	<-done
	return
}

func TestServerAuthSuccess(t *testing.T) {
	cPriv, cPub := genKey(t)
	hPriv, hPub := genKey(t)
	authorized := func(p ed25519.PublicKey) bool { return p.Equal(cPub) }

	got, cErr, sErr := runAuth(cPriv, cPub, hPub, hPriv, hPub, authorized, nil)
	if cErr != nil || sErr != nil {
		t.Fatalf("auth failed: client=%v server=%v", cErr, sErr)
	}
	if !got.Equal(cPub) {
		t.Fatal("server returned wrong client key")
	}
}

func TestServerAuthUnauthorizedClient(t *testing.T) {
	cPriv, cPub := genKey(t)
	hPriv, hPub := genKey(t)
	deny := func(ed25519.PublicKey) bool { return false }

	_, cErr, sErr := runAuth(cPriv, cPub, hPub, hPriv, hPub, deny, nil)
	if sErr == nil {
		t.Fatal("server should reject an unauthorized client")
	}
	if cErr == nil {
		t.Fatal("client should observe the failure")
	}
}

func TestClientAuthWrongHostKey(t *testing.T) {
	cPriv, cPub := genKey(t)
	hPriv, hPub := genKey(t)
	_, wrongHostPub := genKey(t)
	authorized := func(p ed25519.PublicKey) bool { return p.Equal(cPub) }

	// Client expects wrongHostPub; host signs with its real key: client rejects.
	_, cErr, _ := runAuth(cPriv, cPub, wrongHostPub, hPriv, hPub, authorized, nil)
	if cErr == nil {
		t.Fatal("client should reject a mismatched host key")
	}
}

func TestServerAuthTamperedSignature(t *testing.T) {
	cPriv, cPub := genKey(t)
	hPriv, hPub := genKey(t)
	authorized := func(p ed25519.PublicKey) bool { return p.Equal(cPub) }

	// Flip a bit in the client's signature frame body. writeFrame emits a
	// 2-byte header then the body as separate writes; the signature body is the
	// only ed25519.SignatureSize-byte write the client makes.
	tamper := func(b []byte) {
		if len(b) == ed25519.SignatureSize {
			b[len(b)-1] ^= 0x01
		}
	}
	_, _, sErr := runAuth(cPriv, cPub, hPub, hPriv, hPub, authorized, tamper)
	if sErr == nil {
		t.Fatal("server should reject a tampered client signature")
	}
}

// tamperingRW lets a test mutate outbound frames before they hit the wire.
type tamperingRW struct {
	io.ReadWriter
	mutate func([]byte)
}

func (t *tamperingRW) Write(b []byte) (int, error) {
	cp := append([]byte(nil), b...)
	t.mutate(cp)
	return t.ReadWriter.Write(cp)
}
