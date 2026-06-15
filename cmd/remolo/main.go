// Command remolo is a single binary that runs in two roles, host and client,
// and autoconfigures: start a host, copy the token it prints, and from another
// machine run `remolo connect <token>` to land in a real shell. Everything in
// between (endpoint discovery, encrypted transport selection, authentication)
// arranges itself.
package main

import (
	"fmt"
	"os"

	"github.com/mirkobrombin/go-cli-builder/v2/pkg/cli"
	"github.com/mirkobrombin/remolo/internal/session"
)

// CLI is the root command tree.
type CLI struct {
	Host     HostCmd     `cmd:"" help:"Start a remolo host and print a shareable session token"`
	Connect  ConnectCmd  `cmd:"" help:"Connect to a host with a token and open an interactive shell"`
	Exec     ExecCmd     `cmd:"" help:"Run a one-shot command on the host"`
	Put      PutCmd      `cmd:"" help:"Upload a file to the host (resumable)"`
	Get      GetCmd      `cmd:"" help:"Download a file from the host (resumable)"`
	Sessions SessionsCmd `cmd:"" help:"List active reusable connections (mux daemons)"`
	Mux      MuxCmd      `cmd:"" help:"Inspect/close connection-sharing mux daemons"`
	Alias    AliasCmd    `cmd:"" help:"Manage saved host aliases (use a name instead of a token)"`
	Group    GroupCmd    `cmd:"" help:"Manage host groups for fleet exec (remolo exec @group)"`
	Enroll   EnrollCmd   `cmd:"" help:"Enroll this client key with a host (connect later without a token)"`
	Revoke   RevokeCmd   `cmd:"" help:"Revoke an enrolled client key (run on the host)"`
	Token    TokenCmd    `cmd:"" help:"Inspect a token without connecting"`
	cli.Base
}

func main() {
	// quic-go prints a one-line warning to stdout when the OS receive buffer is
	// small. It is cosmetic and pollutes command output, so silence it (the user
	// cannot raise net.core.rmem_max without root anyway).
	if os.Getenv("QUIC_GO_DISABLE_RECEIVE_BUFFER_WARNING") == "" {
		os.Setenv("QUIC_GO_DISABLE_RECEIVE_BUFFER_WARNING", "true")
	}

	// `exec` is special: the command tail may contain dash-flags (ls -la) and an
	// optional `--` separator, which the declarative flag parser would reject.
	// Intercept it and pass the tail through verbatim.
	argv := os.Args[1:]
	if len(argv) >= 1 && argv[0] == "exec" {
		os.Exit(runExecRaw(argv[1:]))
	}

	app, err := cli.New(&CLI{}, cli.WithVersion(session.Version))
	if err != nil {
		fmt.Fprintf(os.Stderr, "remolo: %v\n", err)
		os.Exit(1)
	}
	app.SetName("remolo")
	if err := app.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "remolo: %v\n", err)
		os.Exit(1)
	}
}
