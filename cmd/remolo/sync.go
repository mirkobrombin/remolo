package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/mirkobrombin/go-cli-builder/v2/pkg/cli"
	"github.com/mirkobrombin/remolo/internal/dirsync"
	"github.com/mirkobrombin/remolo/internal/protocol"
	"github.com/mirkobrombin/remolo/internal/session"
)

// SyncCmd performs an incremental (rsync-like) directory sync over the remolo
// connection. By default it pushes a local directory to the host; --pull
// reverses the direction.
type SyncCmd struct {
	Token   string   `arg:"" required:"true" help:"The session token"`
	Local   string   `arg:"" required:"true" help:"Local directory"`
	Remote  string   `arg:"" required:"true" help:"Remote directory (relative to the host file root)"`
	Pull    bool     `cli:"pull" help:"Pull from remote to local instead of pushing"`
	Delete  bool     `cli:"delete" help:"Delete destination files not present in the source"`
	DryRun  bool     `cli:"dry-run" help:"Show what would transfer without writing"`
	Exclude []string `cli:"exclude" help:"Glob patterns to skip (repeatable)"`

	cli.Base
}

func (c *SyncCmd) Run() error {
	tok, err := decodeToken(c.Token)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs, err := openChannel(ctx, tok, protocol.Open{Kind: protocol.KindSync}, session.ConnectOptions{}, c.Logger, false)
	if err != nil {
		return err
	}
	defer cs.cleanup()

	opt := dirsync.Options{Delete: c.Delete, DryRun: c.DryRun, Excludes: c.Exclude}
	var stats dirsync.Stats
	var verb string
	if c.Pull {
		verb = "pulled"
		stats, err = dirsync.Pull(cs.stream, c.Remote, c.Local, opt)
	} else {
		verb = "pushed"
		stats, err = dirsync.Push(cs.stream, c.Local, c.Remote, opt)
	}
	if err != nil {
		return err
	}

	dry := ""
	if c.DryRun {
		dry = " (dry-run)"
		verb = "would have " + strings.TrimSuffix(verb, "ed") + "ed"
	}
	fmt.Printf("remolo: %s %d/%d files, %d bytes%s\n", verb, stats.FilesTransferred, stats.FilesConsidered, stats.BytesTransferred, dry)
	return nil
}
