//go:build linux

package desktop

import (
	"errors"
	"os"
	"strings"
)

// ErrCaptureUnavailable is returned by NewCapturer when no real screen-capture
// backend can be initialised (for example a headless CI box with no display).
// The host treats it as the signal to fall back to NewSyntheticCapturer.
var ErrCaptureUnavailable = errors.New(
	"desktop: screen capture not available on this system")

// NewCapturer returns a real, cgo-free screen capturer for the active Linux
// session, selecting the backend at runtime:
//
//  1. Wayland (WAYLAND_DISPLAY set, or XDG_SESSION_TYPE=wayland): capture via
//     the xdg-desktop-portal Screenshot interface over D-Bus, with a wlroots
//     `grim` fallback.
//  2. X11 (DISPLAY set): grab the root window with the pure-Go xgb XCB client.
//  3. Neither: ErrCaptureUnavailable, so the host degrades to synthetic capture.
func NewCapturer() (Capturer, error) {
	if isWayland() {
		c, err := newWaylandCapturer()
		if err == nil {
			return c, nil
		}
		// A Wayland session usually has no usable X server, but XWayland may be
		// present; try X11 before giving up.
		if os.Getenv("DISPLAY") != "" {
			if xc, xerr := newX11Capturer(); xerr == nil {
				return xc, nil
			}
		}
		return nil, err
	}

	if os.Getenv("DISPLAY") != "" {
		return newX11Capturer()
	}

	return nil, ErrCaptureUnavailable
}

func isWayland() bool {
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		return true
	}
	return strings.EqualFold(os.Getenv("XDG_SESSION_TYPE"), "wayland")
}
