package session

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"net"
	"time"

	"github.com/mirkobrombin/remolo/internal/discovery/mdns"
	"github.com/mirkobrombin/remolo/internal/relay"
	"github.com/mirkobrombin/remolo/internal/rendezvous"
	"github.com/mirkobrombin/remolo/internal/token"
	"github.com/mirkobrombin/remolo/internal/transport/holepunch"
	"github.com/mirkobrombin/remolo/internal/transport/tcpmux"
)

// reflexiveCandidate asks a STUN server for our public address and pairs the
// returned IP with the local listening port. This is best-effort: it lands a
// usable candidate on full-cone or port-preserving NATs, and simply contributes
// nothing on symmetric NATs (where the relay rung takes over).
func reflexiveCandidate(stunServer string, port uint16) *token.Candidate {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	addr, err := holepunch.ReflexiveAddr(ctx, stunServer)
	if err != nil {
		return nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil
	}
	if net.ParseIP(host) == nil {
		return nil
	}
	return &token.Candidate{IP: host, Port: port}
}

// startServices launches the optional discovery and reachability services
// (mDNS advertisement, rendezvous registration, relay registration). Each
// registers a stopper so Close tears it down.
func (h *Host) startServices(ctx context.Context) {
	sessionHex := hex.EncodeToString(h.tok.SessionID)

	if h.opts.MDNS {
		txt := map[string]string{"session": sessionHex, "v": Version}
		if closer, err := mdns.Advertise(h.caps.Hostname, uint16(h.Port()), txt); err == nil {
			h.AddListener(func() { closer.Close() })
			h.log.Info("remolo: mDNS advertisement active (_remolo._udp.local)")
		} else {
			h.log.Warning("remolo: mDNS not started: %v", err)
		}
	}

	if h.opts.Rendezvous != "" {
		go h.registerRendezvous(ctx, h.opts.Rendezvous, sessionHex)
	}

	if h.opts.Relay != "" {
		go h.registerRelay(ctx, h.opts.Relay, sessionHex)
	}
}

// registerRendezvous publishes our candidates to the broker and refreshes them
// until ctx ends, so a client can find us by session id alone.
func (h *Host) registerRendezvous(ctx context.Context, url, sessionHex string) {
	tick := time.NewTicker(2 * time.Minute)
	defer tick.Stop()
	publish := func() {
		if err := rendezvous.Register(url, sessionHex, h.tok.Candidates); err != nil {
			h.log.Warning("remolo: rendezvous register failed: %v", err)
		}
	}
	publish()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			publish()
		}
	}
}

// registerRelay keeps a standing registration with the relay: it waits to be
// paired with a client, wraps the relayed byte stream in the host's pinned TLS
// and a yamux session, and feeds it through the normal authenticated handler.
// End-to-end encryption is preserved; the relay only sees ciphertext.
func (h *Host) registerRelay(ctx context.Context, relayAddr, sessionHex string) {
	// The relay keeps a single standing registration per session id (a second
	// one displaces the first), so we register, serve that relayed peer to
	// completion, then register afresh. This serialises relay peers, which is an
	// acceptable trade for a last-resort fallback.
	for ctx.Err() == nil {
		raw, err := relay.ListenRelay(ctx, relayAddr, sessionHex)
		if err != nil {
			h.log.Warning("remolo: relay unreachable (%v), retrying in 5s", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}
		// Blocks until this relayed connection ends, then we re-register.
		h.handleRelayed(ctx, raw)
	}
}

func (h *Host) handleRelayed(ctx context.Context, raw net.Conn) {
	tlsConn := tls.Server(raw, h.id.ServerTLSConfig())
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		raw.Close()
		return
	}
	conn, err := tcpmux.NewServerConn(tlsConn)
	if err != nil {
		raw.Close()
		return
	}
	h.handleConn(ctx, conn)
}
