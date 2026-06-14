package main

import (
	"fmt"
	"time"

	"github.com/mirkobrombin/go-cli-builder/v2/pkg/cli"
	"github.com/mirkobrombin/remolo/internal/token"
)

// TokenCmd inspects a token without connecting, for debugging and support.
type TokenCmd struct {
	Token string `arg:"" required:"true" help:"The token to inspect"`

	cli.Base
}

func (c *TokenCmd) Run() error {
	tok, err := token.Decode(c.Token)
	if err != nil {
		return err
	}
	fmt.Printf("version:    %d\n", tok.Version)
	fmt.Printf("session id: %x\n", tok.SessionID)
	fmt.Printf("host key:   %x\n", tok.HostPubKey)
	if tok.Expired() {
		fmt.Printf("expiry:     %s (EXPIRED)\n", time.Unix(tok.Expiry, 0).Format(time.RFC3339))
	} else {
		fmt.Printf("expiry:     %s (in %s)\n", time.Unix(tok.Expiry, 0).Format(time.RFC3339), tok.ExpiresIn().Round(time.Second))
	}
	if tok.Rendezvous != "" {
		fmt.Printf("rendezvous: %s\n", tok.Rendezvous)
	}
	fmt.Printf("candidates (%d):\n", len(tok.Candidates))
	for _, cand := range tok.Candidates {
		fmt.Printf("  - %s\n", cand.Addr())
	}
	return nil
}
