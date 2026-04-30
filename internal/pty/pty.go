// Package pty abstracts an operating system pseudo-terminal so the rest of
// remolo can spawn a real interactive shell without caring about the platform.
//
// Unix is backed by creack/pty, Windows by the native ConPTY API. The
// interface is intentionally small: a PTY is a byte stream you can resize and
// whose process you can wait on.
package pty

import "io"

// Config describes the process to launch behind a pseudo-terminal.
type Config struct {
	// Command is the argv to run. If empty, the user's login shell is used.
	Command []string
	// Env are extra environment variables (KEY=VALUE), merged over the host's.
	Env []string
	// Term is the TERM value to advertise (defaults to xterm-256color).
	Term string
	// Cols and Rows set the initial window size.
	Cols, Rows uint16
	// Dir is the working directory (defaults to the user's home).
	Dir string
}

// PTY is a running pseudo-terminal: read its output, write its input, resize
// its window, and wait for the process to exit.
type PTY interface {
	io.ReadWriteCloser
	// Resize changes the terminal window size.
	Resize(cols, rows uint16) error
	// Wait blocks until the process exits and returns its exit code.
	Wait() (int, error)
}

func defaultTerm(t string) string {
	if t == "" {
		return "xterm-256color"
	}
	return t
}
