package session

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"time"

	"github.com/mirkobrombin/remolo/internal/crypto"
	"github.com/mirkobrombin/remolo/internal/discovery/mdns"
	"github.com/mirkobrombin/remolo/internal/relay"
	"github.com/mirkobrombin/remolo/internal/rendezvous"
	"github.com/mirkobrombin/remolo/internal/token"
	"github.com/mirkobrombin/remolo/internal/transport"
	"github.com/mirkobrombin/remolo/internal/transport/sshfallback"
	"github.com/mirkobrombin/remolo/internal/transport/tcpmux"
	"golang.org/x/crypto/ssh"
)

// buildAttempts assembles the ranked ladder of connection strategies for a
// token under the given options. Equal-quality attempts preserve insertion
// order, so QUIC is tried just before TCP on the same endpoint.
func buildAttempts(ctx context.Context, tok *token.Token, opts ConnectOptions) []transport.Attempt {
	tlsConf := crypto.ClientTLSConfig(tok.HostPubKey)
	var attempts []transport.Attempt

	addDirect := func(addr string, quality transport.QualityHint, tag string) {
		attempts = append(attempts, transport.Attempt{
			Label:   tag + " " + addr,
			Quality: quality,
			Dial: func(ctx context.Context) (transport.Conn, error) {
				return transport.DialQUIC(ctx, addr, tlsConf)
			},
		})
		attempts = append(attempts, transport.Attempt{
			Label:   "tcp " + addr,
			Quality: quality,
			Dial: func(ctx context.Context) (transport.Conn, error) {
				return tcpmux.DialTCP(ctx, addr, tlsConf)
			},
		})
	}

	// Rung 1: every endpoint carried in the token, QUIC then TCP.
	for _, c := range tok.Candidates {
		addDirect(c.Addr(), transport.QualityDirect, "direct")
	}

	// Rung 2: mDNS discovery on the local segment.
	if opts.MDNS {
		bctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		entries, _ := mdns.Browse(bctx, 2*time.Second)
		cancel()
		for _, e := range entries {
			if e.AddrPort != "" {
				addDirect(e.AddrPort, transport.QualityLocalDisc, "mdns")
			}
		}
	}

	// Rung 3: rendezvous lookup by session id (broker from token or options).
	rvURL := opts.Rendezvous
	if rvURL == "" {
		rvURL = tok.Rendezvous
	}
	if rvURL != "" {
		if cands, err := rendezvous.Lookup(rvURL, hex.EncodeToString(tok.SessionID)); err == nil {
			for _, c := range cands {
				addDirect(c.Addr(), transport.QualityHolePunch, "rendezvous")
			}
		}
	}

	// Rung 4: relay (E2E preserved; the relay only sees ciphertext).
	if opts.Relay != "" {
		relayAddr := opts.Relay
		sessionHex := hex.EncodeToString(tok.SessionID)
		attempts = append(attempts, transport.Attempt{
			Label:   "relay " + relayAddr,
			Quality: transport.QualityRelay,
			Dial: func(ctx context.Context) (transport.Conn, error) {
				return relay.DialRelay(ctx, relayAddr, sessionHex, tlsClientConf(tok))
			},
		})
	}

	// Rung 5: SSH tunnel into a remolo TCP endpoint reachable from the sshd.
	if opts.SSH != nil {
		s := opts.SSH
		attempts = append(attempts, transport.Attempt{
			Label:   "ssh " + s.Addr,
			Quality: transport.QualitySSH,
			Dial: func(ctx context.Context) (transport.Conn, error) {
				return dialSSH(ctx, s, tlsConf)
			},
		})
	}

	return attempts
}

// tlsClientConf returns the pinned client TLS config for end-to-end encryption
// over relayed/tunnelled byte streams.
func tlsClientConf(tok *token.Token) *tls.Config {
	return crypto.ClientTLSConfig(tok.HostPubKey)
}

func dialSSH(ctx context.Context, s *SSHRung, _ *tls.Config) (transport.Conn, error) {
	auth, err := sshfallback.PublicKeyAuthFromFile(s.KeyPath)
	if err != nil {
		return nil, err
	}
	cb, err := sshfallback.KnownHostsCallback(s.KnownHosts)
	if err != nil {
		return nil, err
	}
	target := s.Target
	if target == "" {
		target = s.Addr
	}
	return sshfallback.DialSSH(ctx, s.Addr, s.User, []ssh.AuthMethod{auth}, cb, target)
}
