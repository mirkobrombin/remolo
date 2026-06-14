package main

import (
	"context"
	"fmt"

	"github.com/mirkobrombin/go-cli-builder/v2/pkg/cli"
	"github.com/mirkobrombin/remolo/internal/filetransfer"
	"github.com/mirkobrombin/remolo/internal/protocol"
	"github.com/mirkobrombin/remolo/internal/session"
)

// PutCmd uploads a local file to the host (resumable).
type PutCmd struct {
	Token  string `arg:"" required:"true" help:"The session token"`
	Local  string `arg:"" required:"true" help:"Local file path"`
	Remote string `arg:"" required:"true" help:"Destination path on the host (relative to its file root)"`

	cli.Base
}

func (c *PutCmd) Run() error {
	tok, err := decodeToken(c.Token)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs, err := openChannel(ctx, tok, protocol.Open{Kind: protocol.KindFile}, session.ConnectOptions{}, c.Logger, false)
	if err != nil {
		return err
	}
	defer cs.cleanup()

	if err := filetransfer.Put(cs.stream, c.Local, c.Remote); err != nil {
		return err
	}
	fmt.Printf("remolo: uploaded %s -> %s\n", c.Local, c.Remote)
	return nil
}

// GetCmd downloads a file from the host (resumable).
type GetCmd struct {
	Token  string `arg:"" required:"true" help:"The session token"`
	Remote string `arg:"" required:"true" help:"Source path on the host (relative to its file root)"`
	Local  string `arg:"" required:"true" help:"Local destination path"`

	cli.Base
}

func (c *GetCmd) Run() error {
	tok, err := decodeToken(c.Token)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs, err := openChannel(ctx, tok, protocol.Open{Kind: protocol.KindFile}, session.ConnectOptions{}, c.Logger, false)
	if err != nil {
		return err
	}
	defer cs.cleanup()

	n, err := filetransfer.Get(cs.stream, c.Remote, c.Local)
	if err != nil {
		return err
	}
	fmt.Printf("remolo: downloaded %s -> %s (%d bytes)\n", c.Remote, c.Local, n)
	return nil
}
