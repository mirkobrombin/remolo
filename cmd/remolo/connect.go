package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/mirkobrombin/go-cli-builder/v2/pkg/cli"
	"github.com/mirkobrombin/remolo/internal/config"
	"github.com/mirkobrombin/remolo/internal/protocol"
	"github.com/mirkobrombin/remolo/internal/session"
	"github.com/mirkobrombin/remolo/internal/token"
)

// ConnectCmd opens an interactive remote shell.
type ConnectCmd struct {
	Token string `arg:"" required:"true" help:"The session token printed by 'remolo host'"`

	MDNS         bool   `cli:"mdns" help:"Also browse the local network via mDNS"`
	Rendezvous   string `cli:"rendezvous" help:"Rendezvous broker URL to look the host up by session id"`
	Relay        string `cli:"relay" help:"Relay server address to fall back to (host:port)"`
	SSHAddr      string `cli:"ssh-addr" help:"SSH tunnel: sshd address (host:port)"`
	SSHUser      string `cli:"ssh-user" help:"SSH tunnel: user"`
	SSHKey       string `cli:"ssh-key" help:"SSH tunnel: private key path"`
	SSHTarget    string `cli:"ssh-target" help:"SSH tunnel: remolo TCP endpoint reachable from the sshd"`
	Record       string `cli:"record" help:"Record the session to an asciinema .cast file"`
	Jump         string `cli:"jump,J" help:"Route through a bastion host (token or alias), keeping E2E"`
	ForwardAgent bool   `cli:"forward-agent,A" help:"Forward the local SSH agent to the remote shell"`
	Resume       bool   `cli:"resume" help:"Roaming: keep the shell alive and reconnect across network changes"`

	cli.Base
}

func (c *ConnectCmd) ladderOptions() session.ConnectOptions {
	opts := session.ConnectOptions{
		MDNS:       c.MDNS,
		Rendezvous: c.Rendezvous,
		Relay:      c.Relay,
	}
	if c.SSHAddr != "" {
		opts.SSH = &session.SSHRung{
			Addr:    c.SSHAddr,
			User:    c.SSHUser,
			KeyPath: c.SSHKey,
			Target:  c.SSHTarget,
		}
	}
	return opts
}

func (c *ConnectCmd) Run() error {
	cols, rows := terminalSize()
	open := protocol.Open{
		Kind: protocol.KindPTY,
		Cols: cols,
		Rows: rows,
		Term: os.Getenv("TERM"),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Enrolled descriptor alias: connect with the client key, no token needed.
	if _, alias, _ := config.Resolve(c.Token); alias != nil && alias.HostKey != "" {
		return c.runViaKey(ctx, *alias, open)
	}

	tok, err := decodeToken(c.Token)
	if err != nil {
		return err
	}

	// ProxyJump: route through a bastion, keeping end-to-end encryption.
	if c.Jump != "" {
		return c.runViaJump(ctx, tok, open)
	}

	// SSH agent forwarding needs a dedicated client (control + reverse channels).
	if c.ForwardAgent {
		return c.runWithAgent(ctx, tok, open)
	}

	// Roaming: a resumable shell that survives network changes.
	if c.Resume {
		return c.runRoaming(ctx, tok, open)
	}

	opts := c.ladderOptions()
	cs, err := openChannel(ctx, tok, open, opts, c.Logger, true)
	if err != nil {
		// Last resort: ask the operator for an address and try once more.
		cs, err = c.manualFallback(ctx, tok, open, opts)
		if err != nil {
			return err
		}
	}
	defer cs.cleanup()

	if !cs.reused && !cs.caps.PTY {
		return fmt.Errorf("the host (%s) does not offer a PTY shell", cs.caps.OS)
	}

	var rec *castRecorder
	if c.Record != "" {
		if rec, err = newCastRecorder(c.Record, cols, rows); err != nil {
			return fmt.Errorf("record: %w", err)
		}
		defer rec.Close()
		fmt.Printf("remolo: recording session to %s\n", c.Record)
	}
	var recW io.Writer
	if rec != nil {
		recW = rec
	}
	code, err := runInteractiveShellRec(cs.stream, recW)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nremolo: session ended (%v)\n", err)
	}
	if code != 0 {
		os.Exit(code)
	}
	return nil
}

// runViaJump connects to the target through a bastion host and runs the shell.
func (c *ConnectCmd) runViaJump(ctx context.Context, tok *token.Token, open protocol.Open) error {
	jumpTok, err := decodeToken(c.Jump)
	if err != nil {
		return fmt.Errorf("jump: %w", err)
	}
	cl, err := session.ConnectViaJump(ctx, tok, jumpTok)
	if err != nil {
		return err
	}
	defer cl.Close("session ended")
	fmt.Printf("remolo: connected %s\n", cl.Route())
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

// manualFallback is the very last rung: remolo could not reach the host
// automatically, so it asks the operator for an address and retries.
func (c *ConnectCmd) manualFallback(ctx context.Context, tok *token.Token, open protocol.Open, opts session.ConnectOptions) (*channelSession, error) {
	fmt.Println("\nremolo: could not reach the host automatically.")
	reader := bufio.NewReader(os.Stdin)
	for attempt := 0; attempt < 3; attempt++ {
		fmt.Print("Enter the host's public IP[:port] (press enter to cancel): ")
		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			return nil, fmt.Errorf("no reachable host")
		}
		cand, perr := parseManualCandidate(line, tok)
		if perr != nil {
			fmt.Printf("  %v\n", perr)
			continue
		}
		// Retry with the manual candidate prepended.
		manual := *tok
		manual.Candidates = append([]token.Candidate{cand}, tok.Candidates...)
		cs, err := openChannel(ctx, &manual, open, opts, c.Logger, true)
		if err == nil {
			return cs, nil
		}
		fmt.Printf("  still unreachable: %v\n", err)
	}
	return nil, fmt.Errorf("host unreachable after manual attempts")
}

// parseManualCandidate turns user input into a candidate, defaulting the port to
// the one already advertised in the token when only an IP is given.
func parseManualCandidate(in string, tok *token.Token) (token.Candidate, error) {
	host := in
	port := defaultPort(tok)
	if i := strings.LastIndex(in, ":"); i >= 0 && !strings.Contains(in[i+1:], "]") {
		host = strings.Trim(in[:i], "[]")
		p, err := strconv.Atoi(in[i+1:])
		if err != nil {
			return token.Candidate{}, fmt.Errorf("invalid port")
		}
		port = uint16(p)
	}
	host = strings.Trim(host, "[]")
	return token.Candidate{IP: host, Port: port}, nil
}

func defaultPort(tok *token.Token) uint16 {
	if len(tok.Candidates) > 0 {
		return tok.Candidates[0].Port
	}
	return 0
}
