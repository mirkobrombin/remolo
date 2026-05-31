//go:build !linux

package desktop

import "errors"

// ErrInjectUnsupported is returned by NewUinputInjector off Linux.
var ErrInjectUnsupported = errors.New(
	"desktop: input injection not supported on this platform")

// NewUinputInjector is Linux-only. On other platforms it always returns
// ErrInjectUnsupported so the desktop channel degrades to view-only.
func NewUinputInjector() (Injector, error) {
	return nil, ErrInjectUnsupported
}
