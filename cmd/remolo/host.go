package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mirkobrombin/go-cli-builder/v2/pkg/cli"
	"github.com/mirkobrombin/remolo/internal/session"
)

// HostCmd starts the controllable side.
type HostCmd struct {
	Bind       string        `cli:"bind,b" help:"Address to bind (default 0.0.0.0:0, ephemeral port)"`
	TTL        time.Duration `cli:"ttl" help:"Token validity window (default 24h)"`
	Loopback   bool          `cli:"loopback" help:"Also advertise 127.0.0.1 (useful for same-machine testing)"`
	Once       bool          `cli:"once" help:"Single-use: admit one authenticated peer, then refuse"`
	Identity   string        `cli:"identity" help:"Use a persistent host key at this path (stable identity across restarts)"`
	MDNS       bool          `cli:"mdns" help:"Advertise on the local network via mDNS"`
	STUN       string        `cli:"stun" help:"STUN server for a public reflexive candidate (host:port)"`
	Rendezvous string        `cli:"rendezvous" help:"Rendezvous broker URL to register with"`
	Relay      string        `cli:"relay" help:"Relay server address to register with (host:port)"`
	FileRoot   string        `cli:"file-root" help:"Directory exposed to file transfer (default: home)"`
	Cap        []string      `cli:"cap" help:"Restrict to these channel kinds: pty,exec,file,sync,rpc,desktop,forward (repeatable)"`
	Cmd        []string      `cli:"cmd" help:"Whitelist exec/pty commands by basename (repeatable)"`
	ReadOnly   bool          `cli:"ro" help:"Read-only: forbid exec, pty, forward, uploads, and sync push"`
	Approve    bool          `cli:"approve" help:"Prompt to accept each new connection"`

	cli.Base
}

func (c *HostCmd) Run() error {
	h, err := session.NewHost(session.HostOptions{
		Bind:            c.Bind,
		TTL:             c.TTL,
		IncludeLoopback: c.Loopback,
		Once:            c.Once,
		Identity:        c.Identity,
		Approve:         c.Approve,
		MDNS:            c.MDNS,
		STUN:            c.STUN,
		Rendezvous:      c.Rendezvous,
		Relay:           c.Relay,
		FileRoot:        c.FileRoot,
		Caps:            c.Cap,
		CmdAllow:        c.Cmd,
		ReadOnly:        c.ReadOnly,
		Logger:          c.Logger,
	})
	if err != nil {
		return err
	}
	defer h.Close()

	ttl := c.TTL
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}

	fmt.Printf("remolo host active, listening on UDP port %d\n", h.Port())
	fmt.Println()
	fmt.Println("Session token:")
	fmt.Printf("   %s\n", h.Token())
	fmt.Println()
	fmt.Printf("Share this token. It expires in %s. (use --ttl to change)\n", ttl.Round(time.Second))
	fmt.Printf("Advertised endpoints: ")
	for i, cand := range h.Candidates() {
		if i > 0 {
			fmt.Print(", ")
		}
		fmt.Print(cand.Addr())
	}
	fmt.Println()
	fmt.Println()
	fmt.Println("Authorized use only: connect machines you own or have explicit consent for.")
	fmt.Println("Waiting for connections... (Ctrl-C to stop)")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	err = h.Serve(ctx)
	if ctx.Err() != nil {
		fmt.Println("\nremolo host stopped.")
		return nil
	}
	return err
}
