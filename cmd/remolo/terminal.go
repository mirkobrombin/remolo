package main

import (
	"io"
	"os"

	"github.com/mirkobrombin/remolo/internal/protocol"
	"github.com/mirkobrombin/remolo/internal/session"
	"github.com/mirkobrombin/remolo/internal/transport"
	"golang.org/x/term"
)

// runInteractiveShell puts the local terminal in raw mode and bridges it to the
// remote PTY channel, forwarding window resizes, until the remote shell exits.
// It returns the remote exit code.
func runInteractiveShell(stream transport.Stream) (int, error) {
	return runInteractiveShellRec(stream, nil)
}

// runInteractiveShellRec is runInteractiveShell with an optional recorder that
// receives a copy of the terminal output (for asciinema session recording).
func runInteractiveShellRec(stream transport.Stream, record io.Writer) (int, error) {
	fd := int(os.Stdin.Fd())

	var restore func()
	if term.IsTerminal(fd) {
		oldState, err := term.MakeRaw(fd)
		if err == nil {
			restore = func() { term.Restore(fd, oldState) }
		}
	}
	if restore != nil {
		defer restore()
	}

	// Forward terminal resizes (platform-specific: SIGWINCH on Unix, polling on
	// Windows).
	resize := make(chan protocol.Resize, 1)
	stopResize := installResizeHandler(fd, resize)
	defer stopResize()

	var out io.Writer = os.Stdout
	if record != nil {
		out = io.MultiWriter(os.Stdout, record)
	}
	code, err := session.BridgeIO(stream, os.Stdin, out, resize)
	close(resize)
	return code, err
}

// terminalSize returns the current terminal size, or a sane default.
func terminalSize() (cols, rows uint16) {
	if w, h, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		return uint16(w), uint16(h)
	}
	return 80, 24
}
