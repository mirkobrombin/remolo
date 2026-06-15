package main

import (
	"fmt"

	"github.com/mirkobrombin/go-cli-builder/v2/pkg/cli"
	"github.com/mirkobrombin/remolo/internal/session"
)

// MuxCmd inspects and controls the connection-sharing mux daemons (remolo's
// equivalent of SSH ControlMaster).
type MuxCmd struct {
	Ls    MuxLsCmd    `cmd:"" help:"List active mux daemons (same as 'remolo sessions')"`
	Close MuxCloseCmd `cmd:"" help:"Close a mux daemon by session id prefix"`

	cli.Base
}

// MuxLsCmd lists active mux daemons.
type MuxLsCmd struct {
	cli.Base
}

func (c *MuxLsCmd) Run() error { return printSessions() }

// MuxCloseCmd shuts down a mux daemon.
type MuxCloseCmd struct {
	ID string `arg:"" required:"true" help:"Session id (or unique prefix) to close"`

	cli.Base
}

func (c *MuxCloseCmd) Run() error {
	info, err := session.CloseDaemon(c.ID)
	if err != nil {
		return err
	}
	fmt.Printf("remolo: closed mux for %s (%s), pid %d\n", info.SessionID, hostLabel(info), info.PID)
	return nil
}
