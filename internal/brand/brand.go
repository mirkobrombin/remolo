// Package brand carries the assets the locally served pages need, so the
// browser tab for a desktop or terminal session is recognisable.
package brand

import _ "embed"

// Icon is the remolo mark as a PNG, served as the favicon of the local pages.
//
//go:embed icon.png
var Icon []byte
