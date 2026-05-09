package sshfallback

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/mirkobrombin/remolo/internal/transport/tcpmux"
	"golang.org/x/crypto/ssh"
)

// genSigner returns a fresh in-memory ed25519 ssh signer.
func genSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	return signer
}

// inMemorySSHD runs a minimal sshd that accepts any client, handles
// direct-tcpip channel requests by dialing the requested target, and splices
// bytes between the channel and the target. It returns the listener address,
// the host public key, and a stop function.
func inMemorySSHD(t *testing.T, hostSigner ssh.Signer) (addr string, hostKey ssh.PublicKey, stop func()) {
	t.Helper()
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
			return &ssh.Permissions{}, nil
		},
	}
	cfg.AddHostKey(hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serveSSHConn(c, cfg)
		}
	}()
	return ln.Addr().String(), hostSigner.PublicKey(), func() { ln.Close() }
}

func serveSSHConn(c net.Conn, cfg *ssh.ServerConfig) {
	sconn, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		c.Close()
		return
	}
	defer sconn.Close()
	go ssh.DiscardRequests(reqs)
	for newChan := range chans {
		if newChan.ChannelType() != "direct-tcpip" {
			newChan.Reject(ssh.UnknownChannelType, "only direct-tcpip")
			continue
		}
		go handleDirectTCPIP(newChan)
	}
}

// directTCPIPPayload mirrors the RFC 4254 direct-tcpip channel open payload.
type directTCPIPPayload struct {
	HostToConnect  string
	PortToConnect  uint32
	OriginatorIP   string
	OriginatorPort uint32
}

func handleDirectTCPIP(newChan ssh.NewChannel) {
	var p directTCPIPPayload
	if err := ssh.Unmarshal(newChan.ExtraData(), &p); err != nil {
		newChan.Reject(ssh.ConnectionFailed, "bad payload")
		return
	}
	target := net.JoinHostPort(p.HostToConnect, itoa(p.PortToConnect))
	dst, err := net.Dial("tcp", target)
	if err != nil {
		newChan.Reject(ssh.ConnectionFailed, "dial failed")
		return
	}
	ch, reqs, err := newChan.Accept()
	if err != nil {
		dst.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); io.Copy(dst, ch); dst.Close(); ch.Close() }()
	go func() { defer wg.Done(); io.Copy(ch, dst); dst.Close(); ch.Close() }()
	wg.Wait()
}

func itoa(p uint32) string {
	if p == 0 {
		return "0"
	}
	var b [10]byte
	i := len(b)
	for p > 0 {
		i--
		b[i] = byte('0' + p%10)
		p /= 10
	}
	return string(b[i:])
}

// TestSSHTunnelHandshake stands up an in-memory sshd plus a tcpmux echo
// endpoint, dials through the tunnel with DialSSH, and verifies an echoed
// round-trip over the direct-tcpip channel.
func TestSSHTunnelHandshake(t *testing.T) {
	// remolo TCP endpoint the SSH host will forward to: a tcpmux server that
	// echoes streams. Plain (no TLS) since this exercises only the tunnel.
	endpoint, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("endpoint listen: %v", err)
	}
	defer endpoint.Close()
	go func() {
		raw, err := endpoint.Accept()
		if err != nil {
			return
		}
		conn, err := tcpmux.NewServerConn(raw)
		if err != nil {
			return
		}
		for {
			s, err := conn.AcceptStream(context.Background())
			if err != nil {
				return
			}
			go func() { defer s.Close(); io.Copy(s, s) }()
		}
	}()

	hostSigner := genSigner(t)
	sshAddr, hostKey, stop := inMemorySSHD(t, hostSigner)
	defer stop()

	auth := []ssh.AuthMethod{ssh.PublicKeys(genSigner(t))}
	hostKeyCb := ssh.FixedHostKey(hostKey)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := DialSSH(ctx, sshAddr, "tester", auth, hostKeyCb, endpoint.Addr().String())
	if err != nil {
		t.Fatalf("DialSSH: %v", err)
	}
	defer conn.Close("done")

	s, err := conn.OpenStream(ctx)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer s.Close()
	msg := []byte("tunnelled through sshd")
	if _, err := s.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(s, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(msg) {
		t.Fatalf("got %q want %q", got, msg)
	}
}

// TestConfigConstruction is a compile/skeleton check that the convenience
// helpers build valid values without requiring a live sshd.
func TestConfigConstruction(t *testing.T) {
	cb, err := KnownHostsCallback("")
	if err != nil {
		t.Fatalf("empty known_hosts callback: %v", err)
	}
	if cb == nil {
		t.Fatalf("expected non-nil insecure callback")
	}
	// A nil host-key callback must be rejected by DialSSH before any dial.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := DialSSH(ctx, "127.0.0.1:1", "u", nil, nil, "127.0.0.1:1"); err == nil {
		t.Fatalf("expected error for nil host key callback")
	}
}
