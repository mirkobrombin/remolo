package crypto

import (
	"bytes"
	"crypto/rand"
	"net"
	"testing"
)

func TestIdentityPinning(t *testing.T) {
	id, err := GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	srv := id.ServerTLSConfig()
	if len(srv.Certificates) != 1 {
		t.Fatal("expected one server certificate")
	}
	// Correct pin must accept the host's own certificate.
	cli := ClientTLSConfig(id.PublicKey())
	if err := cli.VerifyPeerCertificate(srv.Certificates[0].Certificate, nil); err != nil {
		t.Fatalf("correct pin rejected: %v", err)
	}
	// A different pin must reject it.
	other, _ := GenerateIdentity()
	bad := ClientTLSConfig(other.PublicKey())
	if err := bad.VerifyPeerCertificate(srv.Certificates[0].Certificate, nil); err == nil {
		t.Fatal("mismatched pin should be rejected")
	}
}

func psk() []byte {
	p := make([]byte, 32)
	rand.Read(p)
	return p
}

// runHandshake drives both sides over an in-memory connection and returns the
// two results (or errors).
func runHandshake(clientPSK, serverPSK, clientHostPub, serverHostPub []byte) (cRes, sRes *HandshakeResult, cErr, sErr error) {
	c, s := net.Pipe()
	done := make(chan struct{})
	go func() {
		sRes, sErr = ServerHandshake(s, serverPSK, serverHostPub)
		close(done)
	}()
	cRes, cErr = ClientHandshake(c, clientPSK, clientHostPub)
	// Close the client end so a server still blocked on a proof that will
	// never arrive (mismatched PSK/key paths) unblocks instead of deadlocking.
	c.Close()
	<-done
	s.Close()
	return
}

func TestHandshakeSuccess(t *testing.T) {
	p := psk()
	id, _ := GenerateIdentity()
	hp := id.PublicKey()
	cRes, sRes, cErr, sErr := runHandshake(p, p, hp, hp)
	if cErr != nil || sErr != nil {
		t.Fatalf("handshake failed: client=%v server=%v", cErr, sErr)
	}
	if !bytes.Equal(cRes.SessionKey, sRes.SessionKey) {
		t.Fatal("session keys differ between peers")
	}
	if len(cRes.SessionKey) != 32 {
		t.Fatalf("unexpected session key length %d", len(cRes.SessionKey))
	}
}

func TestHandshakeWrongPSK(t *testing.T) {
	id, _ := GenerateIdentity()
	hp := id.PublicKey()
	_, _, cErr, sErr := runHandshake(psk(), psk(), hp, hp)
	if cErr == nil && sErr == nil {
		t.Fatal("handshake with mismatched PSK should fail")
	}
}

func TestHandshakeWrongHostKey(t *testing.T) {
	p := psk()
	id1, _ := GenerateIdentity()
	id2, _ := GenerateIdentity()
	// Client expects id2's host key, server proves with id1's: must fail.
	_, _, cErr, _ := runHandshake(p, p, id2.PublicKey(), id1.PublicKey())
	if cErr == nil {
		t.Fatal("handshake with mismatched host key should fail on client")
	}
}
