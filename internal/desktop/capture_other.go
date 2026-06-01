//go:build !linux && !windows && !darwin

package desktop

import "errors"

// ErrCaptureUnavailable is returned by NewCapturer on platforms without a
// real cgo-free capture backend, so the desktop channel negotiates and degrades
// cleanly. Use NewSyntheticCapturer to exercise the pipeline.
var ErrCaptureUnavailable = errors.New(
	"desktop: screen capture not available on this platform")

// NewCapturer always fails on platforms other than Linux, Windows, and macOS in
// this cgo-free build.
func NewCapturer() (Capturer, error) {
	return nil, ErrCaptureUnavailable
}
