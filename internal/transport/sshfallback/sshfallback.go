// Package sshfallback tunnels remolo through an existing sshd using an SSH
// "direct-tcpip" channel. In locked-down environments where only port 22 is
// reachable, the client dials the remote sshd, asks it to forward a TCP
// connection to the remolo endpoint listening on the host's loopback (or LAN),
// and runs the multiplexed remolo transport over that channel.
//
// This is the lowest rung of remolo's degradation ladder (transport.QualitySSH):
// it works wherever ssh works, at the cost of an extra hop and SSH framing.
package sshfallback

import (
	"context"
	"fmt"
	"os"

	"github.com/mirkobrombin/remolo/internal/transport"
	"github.com/mirkobrombin/remolo/internal/transport/tcpmux"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// DialSSH connects to sshAddr (host:port) as user with the given auth methods
// and host-key callback, then opens a direct-tcpip channel to targetTCP (the
// remolo TCP endpoint reachable from the SSH host, e.g. "127.0.0.1:8787") and
// returns a multiplexed transport.Conn over that channel.
//
// The returned Conn owns the SSH client: closing the Conn tears down the
// tunnelled channel; the SSH client is closed alongside it.
func DialSSH(ctx context.Context, sshAddr, user string, auth []ssh.AuthMethod, hostKeyCb ssh.HostKeyCallback, targetTCP string) (transport.Conn, error) {
	if hostKeyCb == nil {
		return nil, fmt.Errorf("sshfallback: nil host key callback")
	}
	config := &ssh.ClientConfig{
		User:            user,
		Auth:            auth,
		HostKeyCallback: hostKeyCb,
	}

	// Honour ctx cancellation during the (potentially slow) dial+handshake by
	// running it in a goroutine.
	type dialResult struct {
		c   *ssh.Client
		err error
	}
	ch := make(chan dialResult, 1)
	go func() {
		c, err := ssh.Dial("tcp", sshAddr, config)
		ch <- dialResult{c: c, err: err}
	}()

	var client *ssh.Client
	select {
	case <-ctx.Done():
		// The orphaned goroutine closes any client it manages to create.
		go func() {
			if r := <-ch; r.c != nil {
				r.c.Close()
			}
		}()
		return nil, ctx.Err()
	case r := <-ch:
		if r.err != nil {
			return nil, fmt.Errorf("sshfallback dial %s: %w", sshAddr, r.err)
		}
		client = r.c
	}

	channel, err := client.Dial("tcp", targetTCP)
	if err != nil {
		client.Close()
		return nil, fmt.Errorf("sshfallback direct-tcpip %s: %w", targetTCP, err)
	}

	conn, err := tcpmux.NewClientConn(channel)
	if err != nil {
		channel.Close()
		client.Close()
		return nil, err
	}
	return &sshConn{Conn: conn, client: client}, nil
}

// sshConn wraps the tcpmux transport.Conn so that closing it also closes the
// owning SSH client.
type sshConn struct {
	transport.Conn
	client *ssh.Client
}

func (s *sshConn) Close(reason string) error {
	err := s.Conn.Close(reason)
	if cerr := s.client.Close(); err == nil {
		err = cerr
	}
	return err
}

// PublicKeyAuthFromFile loads an unencrypted private key from path and returns
// a public-key auth method backed by it.
func PublicKeyAuthFromFile(path string) (ssh.AuthMethod, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("sshfallback read key %s: %w", path, err)
	}
	signer, err := ssh.ParsePrivateKey(pem)
	if err != nil {
		return nil, fmt.Errorf("sshfallback parse key %s: %w", path, err)
	}
	return ssh.PublicKeys(signer), nil
}

// KnownHostsCallback returns a host-key callback that verifies peers against
// the OpenSSH known_hosts file at path.
//
// If path is empty the callback falls back to ssh.InsecureIgnoreHostKey, which
// performs NO host-key verification and is vulnerable to man-in-the-middle
// attacks. It is provided only for development and explicit opt-in; never use
// it against untrusted networks.
func KnownHostsCallback(path string) (ssh.HostKeyCallback, error) {
	if path == "" {
		return ssh.InsecureIgnoreHostKey(), nil
	}
	cb, err := knownhosts.New(path)
	if err != nil {
		return nil, fmt.Errorf("sshfallback known_hosts %s: %w", path, err)
	}
	return cb, nil
}
