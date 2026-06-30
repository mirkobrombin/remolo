package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/mirkobrombin/go-cli-builder/v2/pkg/cli"
	"github.com/mirkobrombin/remolo/internal/session"
)

// SessionsCmd lists active reusable connections (mux daemons).
type SessionsCmd struct {
	JSON bool `cli:"json" help:"Output as JSON for scripting"`

	cli.Base
}

func (c *SessionsCmd) Run() error {
	if c.JSON {
		sessions, err := session.ListSessions()
		if err != nil {
			return err
		}
		b, _ := json.MarshalIndent(sessions, "", "  ")
		fmt.Println(string(b))
		return nil
	}
	return printSessions()
}

func printSessions() error {
	sessions, err := session.ListSessions()
	if err != nil {
		return err
	}
	if len(sessions) == 0 {
		fmt.Println("No active sessions.")
		return nil
	}
	fmt.Printf("%-18s %-20s %-30s %-9s %s\n", "SESSION", "HOST", "ROUTE", "PID", "UPTIME")
	for _, s := range sessions {
		id := s.SessionID
		if len(id) > 16 {
			id = id[:16]
		}
		up := time.Since(s.Started).Round(time.Second)
		fmt.Printf("%-18s %-20s %-30s %-9d %s\n", id, hostLabel(s), s.Route, s.PID, up)
	}
	return nil
}

func hostLabel(s session.SessionInfo) string {
	if s.Hostname != "" {
		return fmt.Sprintf("%s (%s)", s.Hostname, s.OS)
	}
	return s.OS
}
