package main

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"

	"github.com/mirkobrombin/go-cli-builder/v2/pkg/cli"
	"github.com/mirkobrombin/remolo/internal/desktop"
	"github.com/mirkobrombin/remolo/internal/protocol"
	"github.com/mirkobrombin/remolo/internal/session"
)

// SnapshotCmd captures N screenshots of the host's screen and writes them as PNG
// files. For a live interactive remote desktop use 'remolo desktop' instead.
type SnapshotCmd struct {
	Token string `arg:"" required:"true" help:"The session token"`
	Num   int    `cli:"num,n" help:"Number of frames to capture (default 1)"`
	Out   string `cli:"out,o" help:"Directory to write captured PNG frames (default ./remolo-frames)"`
	Diff  bool   `cli:"diff" help:"Use the tile-diff codec instead of full screenshots"`

	cli.Base
}

func (c *SnapshotCmd) Run() error {
	tok, err := decodeToken(c.Token)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cs, err := openChannel(ctx, tok, protocol.Open{Kind: protocol.KindDesktop}, session.ConnectOptions{}, c.Logger, true)
	if err != nil {
		return err
	}
	defer cs.cleanup()

	if !cs.reused && !cs.caps.Desktop {
		fmt.Printf("remolo: the host (%s on %s) does not expose a desktop channel.\n", cs.caps.Hostname, cs.caps.OS)
		return nil
	}

	n := c.Num
	if n <= 0 {
		n = 1
	}
	outDir := c.Out
	if outDir == "" {
		outDir = "remolo-frames"
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	mode := desktop.ModeScreenshot
	if c.Diff {
		mode = desktop.ModeDiff
	}

	captured := 0
	onFrame := func(img *image.RGBA) {
		if captured >= n {
			return
		}
		path := filepath.Join(outDir, fmt.Sprintf("frame-%03d.png", captured))
		f, ferr := os.Create(path)
		if ferr != nil {
			return
		}
		_ = png.Encode(f, img)
		f.Close()
		captured++
		fmt.Printf("remolo: frame %d/%d (%dx%d) saved to %s\n", captured, n, img.Rect.Dx(), img.Rect.Dy(), path)
		if captured >= n {
			cs.stream.Close()
		}
	}

	err = desktop.Run(cs.stream, mode, 10, onFrame, nil)
	if err != nil && captured == 0 {
		fmt.Printf("remolo: desktop channel unavailable (%v).\n", err)
		return nil
	}
	fmt.Printf("remolo: captured %d frame(s) in %s/\n", captured, outDir)
	return nil
}
