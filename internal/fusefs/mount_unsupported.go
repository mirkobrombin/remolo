//go:build !linux && !darwin

package fusefs

// Mount is the unsupported-platform stub. go-fuse only supports linux
// and darwin, so on every other OS Mount returns ErrUnsupportedPlatform
// without touching any FUSE machinery, keeping the package buildable
// everywhere.
func Mount(mountpoint string, b Backend, opt Options) (*Server, error) {
	return nil, ErrUnsupportedPlatform
}

// IsUnavailable reports whether err means FUSE is impossible here. On an
// unsupported platform the only such error is ErrUnsupportedPlatform.
func IsUnavailable(err error) bool {
	return err == ErrUnsupportedPlatform
}
