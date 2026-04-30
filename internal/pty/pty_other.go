//go:build !(linux || darwin || freebsd || netbsd || openbsd || windows)

package pty

import (
	"fmt"
	"runtime"
)

// Supported reports whether this platform can allocate a PTY.
func Supported() bool { return false }

// Start is unavailable here: this file covers the platforms with neither a
// Unix PTY nor ConPTY. The host advertises no PTY capability and the session
// degrades to the other channels.
func Start(cfg Config) (PTY, error) {
	return nil, fmt.Errorf("pty: not supported on %s", runtime.GOOS)
}
