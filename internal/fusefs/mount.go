package fusefs

// Server represents a live FUSE mount. It is returned by Mount and lets
// the caller block until the filesystem is unmounted (Wait) or trigger
// an unmount (Unmount). The concrete server is created by the
// platform-specific Mount implementation; on unsupported platforms Mount
// returns ErrUnsupportedPlatform and never produces a Server.
type Server struct {
	// impl is the platform-specific server handle. On linux/darwin it
	// wraps *fuse.Server; elsewhere it is never set because Mount fails
	// first.
	impl serverImpl
}

// serverImpl is the platform-specific backing for a Server.
type serverImpl interface {
	wait() error
	unmount() error
}

// Wait blocks until the filesystem is unmounted (for example via
// Unmount, or an external "fusermount -u" / "umount"). It returns nil on
// a clean unmount.
func (s *Server) Wait() error {
	return s.impl.wait()
}

// Unmount unmounts the filesystem. After a successful Unmount, a
// concurrent Wait returns.
func (s *Server) Unmount() error {
	return s.impl.unmount()
}
