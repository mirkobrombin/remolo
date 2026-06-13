package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"

	"github.com/mirkobrombin/go-cli-builder/v2/pkg/cli"
	"github.com/mirkobrombin/remolo/internal/config"
	"github.com/mirkobrombin/remolo/internal/identity"
	"github.com/mirkobrombin/remolo/internal/protocol"
	"github.com/mirkobrombin/remolo/internal/rpc"
	"github.com/mirkobrombin/remolo/internal/session"
)

// EnrollCmd registers this client's public key with the host (using a one-shot
// token) and saves a descriptor alias, so future connections need no token.
type EnrollCmd struct {
	Token string `arg:"" required:"true" help:"The session token from the host"`
	As    string `cli:"as" help:"Alias name to save (default: the host's hostname)"`

	cli.Base
}

func (c *EnrollCmd) Run() error {
	tok, err := decodeToken(c.Token)
	if err != nil {
		return err
	}
	priv, pub, err := identity.LoadOrCreateClientKey(identity.DefaultClientKeyPath())
	if err != nil {
		return err
	}
	_ = priv

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs, err := openChannel(ctx, tok, protocol.Open{Kind: protocol.KindRPC}, session.ConnectOptions{}, c.Logger, true)
	if err != nil {
		return err
	}
	defer cs.cleanup()

	host, _ := os.Hostname()
	var res struct{ Ok bool }
	if err := rpc.Call(cs.stream, "enroll", map[string]any{"pubkey": []byte(pub), "label": "remolo client"}, &res); err != nil {
		return fmt.Errorf("enroll: %w (host must run with --identity to keep enrollments across restarts)", err)
	}

	// Save a descriptor alias so we can connect with the client key later.
	name := c.As
	if name == "" {
		name = cs.caps.Hostname
	}
	if name == "" {
		name = "enrolled"
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	cands := make([]string, 0, len(tok.Candidates))
	for _, cd := range tok.Candidates {
		cands = append(cands, cd.Addr())
	}
	cfg.SetAlias(name, config.Alias{
		HostKey:    base64.StdEncoding.EncodeToString(tok.HostPubKey),
		Candidates: cands,
		Rendezvous: tok.Rendezvous,
		Note:       "enrolled " + host,
	})
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Printf("remolo: enrolled. Connect anytime with: remolo connect %s\n", name)
	fmt.Printf("remolo: client key fingerprint %s\n", identity.FingerprintBase64(pub))
	return nil
}

// RevokeCmd removes an enrolled client key from this host's authorized list. Run
// it on the host machine.
type RevokeCmd struct {
	Fingerprint string `arg:"" required:"true" help:"Client key fingerprint (or prefix) to revoke"`

	cli.Base
}

func (c *RevokeCmd) Run() error {
	auth, err := identity.LoadAuthorized(identity.DefaultAuthorizedPath())
	if err != nil {
		return err
	}
	for _, e := range auth.List() {
		fp := identity.FingerprintBase64(e.Pub)
		if fp == c.Fingerprint || (len(c.Fingerprint) >= 6 && len(fp) >= len(c.Fingerprint) && fp[:len(c.Fingerprint)] == c.Fingerprint) {
			auth.Remove(e.Pub)
			fmt.Printf("remolo: revoked client %s\n", fp)
			return nil
		}
	}
	return fmt.Errorf("no enrolled client matching %q", c.Fingerprint)
}

// runViaKey connects using the saved client key and an enrolled descriptor.
func (c *ConnectCmd) runViaKey(ctx context.Context, alias config.Alias, open protocol.Open) error {
	hostPub, err := base64.StdEncoding.DecodeString(alias.HostKey)
	if err != nil || len(hostPub) != 32 {
		return fmt.Errorf("invalid enrolled host key")
	}
	priv, pub, err := identity.LoadOrCreateClientKey(identity.DefaultClientKeyPath())
	if err != nil {
		return err
	}
	cands := candidatesFromStrings(alias.Candidates)
	cl, err := session.ConnectKey(ctx, hostPub, cands, ed25519.PrivateKey(priv), ed25519.PublicKey(pub), nil)
	if err != nil {
		return err
	}
	defer cl.Close("session ended")
	fmt.Printf("remolo: connected via %s (client key)\n", cl.Route())
	stream, err := cl.OpenChannel(ctx, open)
	if err != nil {
		return err
	}
	code, err := runInteractiveShell(stream)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nremolo: session ended (%v)\n", err)
	}
	if code != 0 {
		os.Exit(code)
	}
	return nil
}
