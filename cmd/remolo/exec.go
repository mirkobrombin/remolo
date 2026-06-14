package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/mirkobrombin/go-cli-builder/v2/pkg/cli"
	"github.com/mirkobrombin/go-cli-builder/v2/pkg/log"
	"github.com/mirkobrombin/remolo/internal/config"
	"github.com/mirkobrombin/remolo/internal/protocol"
	"github.com/mirkobrombin/remolo/internal/session"
)

// ExecCmd is registered so `remolo --help` lists exec, but exec is actually
// dispatched by runExecRaw from main (it bypasses flag parsing so the command
// tail can carry its own dash-flags). This Run is a safety net.
type ExecCmd struct {
	Token   string   `arg:"" required:"true" help:"The session token"`
	Command []string `arg:"" required:"true" help:"Command and arguments to run on the host. Flags before the command: --cwd DIR, --env K=V, --env-pass K, -t/--tty, -T, -q"`

	cli.Base
}

func (c *ExecCmd) Run() error {
	return runExecCommand(c.Token, execOptions{}, c.Command)
}

// execOptions captures the flags exec accepts before the command.
type execOptions struct {
	cwd   string
	env   map[string]string
	tty   bool
	noTTY bool
	quiet bool
}

// runExecRaw handles `remolo exec <token> [flags] [--] <cmd...>` with the
// command tail passed through verbatim. Returns a process exit code.
func runExecRaw(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		printExecHelp()
		return 0
	}
	tokenArg := args[0]
	rest := args[1:]

	opts := execOptions{env: map[string]string{}}
	// Parse recognised flags until the command begins (first non-flag) or `--`.
	i := 0
	for i < len(rest) {
		a := rest[i]
		switch {
		case a == "--":
			i++
			goto done
		case a == "-t" || a == "--tty":
			opts.tty = true
			i++
		case a == "-T" || a == "--no-tty":
			opts.noTTY = true
			i++
		case a == "-q" || a == "--quiet":
			opts.quiet = true
			i++
		case a == "--cwd":
			if i+1 >= len(rest) {
				fmt.Fprintln(os.Stderr, "remolo: --cwd needs a value")
				return 2
			}
			opts.cwd = rest[i+1]
			i += 2
		case strings.HasPrefix(a, "--cwd="):
			opts.cwd = strings.TrimPrefix(a, "--cwd=")
			i++
		case a == "--env":
			if i+1 >= len(rest) {
				fmt.Fprintln(os.Stderr, "remolo: --env needs KEY=VALUE")
				return 2
			}
			addEnv(opts.env, rest[i+1])
			i += 2
		case strings.HasPrefix(a, "--env="):
			addEnv(opts.env, strings.TrimPrefix(a, "--env="))
			i++
		case a == "--env-pass":
			if i+1 >= len(rest) {
				fmt.Fprintln(os.Stderr, "remolo: --env-pass needs a KEY")
				return 2
			}
			passEnv(opts.env, rest[i+1])
			i += 2
		case strings.HasPrefix(a, "--env-pass="):
			passEnv(opts.env, strings.TrimPrefix(a, "--env-pass="))
			i++
		default:
			// First unrecognised token starts the command.
			goto done
		}
	}
done:
	command := rest[i:]
	if len(command) == 0 {
		fmt.Fprintln(os.Stderr, "remolo: exec requires a command")
		return 2
	}
	// Fleet exec: "@group" runs the command on every host in the group.
	if strings.HasPrefix(tokenArg, "@") {
		return runFleetExec(strings.TrimPrefix(tokenArg, "@"), opts, command)
	}
	if err := runExecCommand(tokenArg, opts, command); err != nil {
		fmt.Fprintf(os.Stderr, "remolo: %v\n", err)
		return 1
	}
	return 0
}

// runFleetExec runs the command on every alias in a group, in parallel, with
// per-host output.
func runFleetExec(group string, opts execOptions, command []string) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "remolo: %v\n", err)
		return 1
	}
	members, ok := cfg.Group(group)
	if !ok || len(members) == 0 {
		fmt.Fprintf(os.Stderr, "remolo: no group %q (define with 'remolo group set')\n", group)
		return 1
	}

	type result struct {
		host string
		out  string
		err  error
		code int
	}
	results := make([]result, len(members))
	var wg sync.WaitGroup
	for idx, host := range members {
		wg.Add(1)
		go func(idx int, host string) {
			defer wg.Done()
			var buf strings.Builder
			code, err := runExecCapture(host, opts, command, &buf)
			results[idx] = result{host: host, out: buf.String(), err: err, code: code}
		}(idx, host)
	}
	wg.Wait()

	worst := 0
	for _, r := range results {
		fmt.Printf("=== %s ===\n", r.host)
		if r.err != nil {
			fmt.Printf("  error: %v\n", r.err)
			worst = 1
			continue
		}
		fmt.Print(indent(r.out))
		if r.code != 0 {
			fmt.Printf("  [exit %d]\n", r.code)
			worst = r.code
		}
	}
	return worst
}

func indent(s string) string {
	if s == "" {
		return ""
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := range lines {
		lines[i] = "  " + lines[i]
	}
	return strings.Join(lines, "\n") + "\n"
}

func addEnv(m map[string]string, kv string) {
	if k, v, ok := strings.Cut(kv, "="); ok {
		m[k] = v
	}
}

func passEnv(m map[string]string, key string) {
	if v, ok := os.LookupEnv(key); ok {
		m[key] = v
	}
}

func printExecHelp() {
	fmt.Println("Usage: remolo exec <token> [flags] [--] <command> [args...]")
	fmt.Println()
	fmt.Println("Run a one-shot command on the host. The command may include its own flags.")
	fmt.Println()
	fmt.Println("Flags (before the command):")
	fmt.Println("  --cwd DIR        remote working directory")
	fmt.Println("  --env K=V        set a remote environment variable (repeatable)")
	fmt.Println("  --env-pass K     forward a local environment variable (repeatable)")
	fmt.Println("  -t, --tty        force a PTY for the command")
	fmt.Println("  -T, --no-tty     never allocate a PTY (default)")
	fmt.Println("  -q, --quiet      suppress remolo's own progress output")
}

func runExecCommand(tokenArg string, opts execOptions, command []string) error {
	tok, err := decodeToken(tokenArg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	kind := protocol.KindExec
	if opts.tty && !opts.noTTY {
		kind = protocol.KindPTY
	}
	open := protocol.Open{Kind: kind, Command: command, Dir: opts.cwd, Env: opts.env}
	if kind == protocol.KindPTY {
		open.Cols, open.Rows = terminalSize()
		open.Term = os.Getenv("TERM")
	}

	cs, err := openChannel(ctx, tok, open, session.ConnectOptions{}, log.New(), false)
	if err != nil {
		return err
	}
	defer cs.cleanup()

	if kind == protocol.KindPTY {
		code, rerr := runInteractiveShell(cs.stream)
		if rerr != nil {
			return rerr
		}
		if code != 0 {
			os.Exit(code)
		}
		return nil
	}

	// Keep stdout and stderr separate so scripting and 2>/dev/null work.
	code, err := session.BridgeIOErr(cs.stream, os.Stdin, os.Stdout, os.Stderr, nil)
	if err != nil {
		return err
	}
	if code != 0 {
		os.Exit(code)
	}
	return nil
}

// runExecCapture runs an exec channel and writes combined output to w, returning
// the exit code (for fleet exec; never calls os.Exit).
func runExecCapture(tokenArg string, opts execOptions, command []string, w io.Writer) (int, error) {
	tok, err := decodeToken(tokenArg)
	if err != nil {
		return -1, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cs, err := openChannel(ctx, tok, protocol.Open{Kind: protocol.KindExec, Command: command, Dir: opts.cwd, Env: opts.env}, session.ConnectOptions{}, log.New(), false)
	if err != nil {
		return -1, err
	}
	defer cs.cleanup()
	return session.BridgeIOErr(cs.stream, nil, w, w, nil)
}
