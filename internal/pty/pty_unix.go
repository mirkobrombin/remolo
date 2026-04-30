//go:build linux || darwin || freebsd || netbsd || openbsd

package pty

import (
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/creack/pty"
)

// Supported reports whether this platform can allocate a PTY.
func Supported() bool { return true }

type unixPTY struct {
	f   *os.File
	cmd *exec.Cmd
}

// Start launches the configured command attached to a fresh pseudo-terminal.
func Start(cfg Config) (PTY, error) {
	argv := cfg.Command
	if len(argv) == 0 {
		argv = []string{loginShell()}
	}
	cmd := exec.Command(argv[0], argv[1:]...)

	cmd.Env = append(os.Environ(), "TERM="+defaultTerm(cfg.Term))
	cmd.Env = append(cmd.Env, cfg.Env...)
	if cfg.Dir != "" {
		cmd.Dir = cfg.Dir
	} else if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
	}

	ws := &pty.Winsize{Rows: orDefault(cfg.Rows, 24), Cols: orDefault(cfg.Cols, 80)}
	f, err := pty.StartWithSize(cmd, ws)
	if err != nil {
		return nil, fmt.Errorf("pty: start %q: %w", argv[0], err)
	}
	return &unixPTY{f: f, cmd: cmd}, nil
}

func (p *unixPTY) Read(b []byte) (int, error)  { return p.f.Read(b) }
func (p *unixPTY) Write(b []byte) (int, error) { return p.f.Write(b) }

func (p *unixPTY) Resize(cols, rows uint16) error {
	return pty.Setsize(p.f, &pty.Winsize{Rows: rows, Cols: cols})
}

func (p *unixPTY) Wait() (int, error) {
	err := p.cmd.Wait()
	if err == nil {
		return 0, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	return -1, err
}

func (p *unixPTY) Close() error {
	// Closing the master triggers EOF on the slave; also signal the process so
	// a stuck child does not linger.
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	return p.f.Close()
}

func orDefault(v, d uint16) uint16 {
	if v == 0 {
		return d
	}
	return v
}

// loginShell resolves the user's preferred shell, falling back to /bin/sh.
func loginShell() string {
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "/bin/sh"
}
