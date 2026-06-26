package main

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/mirkobrombin/remolo/internal/config"
	"github.com/mirkobrombin/remolo/internal/protocol"
	"github.com/mirkobrombin/remolo/internal/session"
	"github.com/mirkobrombin/remolo/internal/token"
	"github.com/mirkobrombin/remolo/internal/transport"
)

// channelSession is an opened logical channel plus how to tear it down.
type channelSession struct {
	stream  transport.Stream
	reused  bool
	route   string
	caps    protocol.Capabilities
	cleanup func()
}

// openChannel obtains a channel of the requested kind, reusing an existing mux
// daemon for the peer when one is alive (cheap, no new handshake) and otherwise
// establishing the connection from scratch via happy-eyeballs and starting a
// daemon so later invocations can reuse it.
func openChannel(ctx context.Context, tok *token.Token, open protocol.Open, opts session.ConnectOptions, log session.Logger, showProgress bool) (*channelSession, error) {
	// Fast path: a daemon already owns the connection to this peer.
	if session.HasDaemon(tok.SessionID) {
		ch, ok, err := session.DialDaemon(tok.SessionID, open)
		if err == nil && ok {
			if showProgress {
				fmt.Println("remolo: reusing existing connection (mux), opening new channel... ok")
			}
			return &channelSession{stream: ch, reused: true, cleanup: func() { ch.Close() }}, nil
		}
	}

	// Slow path: connect from scratch.
	if showProgress {
		fmt.Print("remolo: resolving host... ")
	}
	var attempts []string
	onResult := func(r transport.Result) {
		mark := "✗"
		extra := ""
		if r.OK {
			mark = "✓"
			extra = fmt.Sprintf(" RTT %s", r.RTT.Round(100*time.Microsecond))
		}
		// The label already names the rung ("direct 10.0.0.2:41000", "relay ..."),
		// so printing the quality alongside it would just stutter.
		attempts = append(attempts, fmt.Sprintf("[%s %s%s]", r.Label, mark, extra))
	}

	cl, err := session.ConnectWith(ctx, tok, opts, onResult)
	if showProgress {
		for _, a := range attempts {
			fmt.Print(a, " ")
		}
	}
	if err != nil {
		if showProgress {
			fmt.Println()
		}
		return nil, err
	}
	if showProgress {
		fmt.Printf("connected via %s\n", cl.Route())
	}

	// Start a mux daemon so sibling commands reuse this connection. If it fails
	// (e.g. another process won the race), carry on without reuse.
	d, derr := session.StartDaemon(cl, tok.SessionID, log)
	if derr != nil {
		log.Warning("remolo: mux daemon not started: %v", derr)
	}

	stream, err := cl.OpenChannel(ctx, open)
	if err != nil {
		if d != nil {
			d.Close()
		}
		cl.Close("channel open failed")
		return nil, err
	}

	cleanup := func() {
		if d != nil {
			d.Close()
		}
		cl.Close("session ended")
	}
	return &channelSession{stream: stream, reused: false, route: cl.Route(), caps: cl.Capabilities(), cleanup: cleanup}, nil
}

// decodeToken parses and sanity-checks a token argument, resolving a saved
// alias name to its token first (so 'remolo connect myhost' works).
func decodeToken(arg string) (*token.Token, error) {
	resolved, alias, _ := config.Resolve(arg)
	tok, err := token.Decode(resolved)
	if err != nil {
		return nil, err
	}
	if tok.Expired() {
		return nil, fmt.Errorf("token expired %s ago", (-tok.ExpiresIn()).Round(time.Second))
	}
	// When connecting by a stable alias, remember the host key (TOFU) and refuse
	// if it ever changes.
	if alias != nil {
		if err := verifyKnownHost(arg, tok.HostPubKey); err != nil {
			return nil, err
		}
	}
	return tok, nil
}

// candidatesFromStrings parses "host:port" strings into token candidates.
func candidatesFromStrings(addrs []string) []token.Candidate {
	out := make([]token.Candidate, 0, len(addrs))
	for _, a := range addrs {
		host, portStr, err := net.SplitHostPort(a)
		if err != nil {
			continue
		}
		p, err := strconv.Atoi(portStr)
		if err != nil {
			continue
		}
		out = append(out, token.Candidate{IP: host, Port: uint16(p)})
	}
	return out
}
