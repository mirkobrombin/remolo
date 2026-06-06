package session

import (
	"os"
	"runtime"

	"github.com/mirkobrombin/remolo/internal/protocol"
	"github.com/mirkobrombin/remolo/internal/pty"
)

// Version is the remolo build version, surfaced in capability negotiation. It
// is a var so release builds can stamp it via -ldflags -X.
var Version = "0.1.0"

// LocalCapabilities reports what this host can offer, so the client can degrade
// gracefully when a feature is missing (no display, no PTY, ...).
func LocalCapabilities() protocol.Capabilities {
	host, _ := os.Hostname()
	return protocol.Capabilities{
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		PTY:      pty.Supported(),
		Exec:     true,
		File:     true,
		Desktop:  true, // capture falls back to synthetic when headless; input is real on Linux
		Hostname: host,
		Version:  Version,
	}
}
