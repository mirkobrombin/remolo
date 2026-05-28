//go:build linux || darwin

package fusefs

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"

	gofs "github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// fuseServer adapts *fuse.Server to the serverImpl interface.
type fuseServer struct {
	srv *fuse.Server
}

func (f *fuseServer) wait() error {
	f.srv.Wait()
	return nil
}

func (f *fuseServer) unmount() error {
	return f.srv.Unmount()
}

// Mount mounts a FUSE filesystem backed by b at mountpoint and starts
// serving it in the background. The returned *Server can be used to wait
// for or trigger an unmount.
//
// If /dev/fuse is unavailable or the process lacks the privileges to
// mount, Mount returns an error wrapping a permission/unavailability
// condition (callers and tests can detect it with IsUnavailable) rather
// than panicking.
func Mount(mountpoint string, b Backend, opt Options) (*Server, error) {
	if b == nil {
		return nil, errors.New("fusefs: nil backend")
	}
	if fi, err := os.Stat(mountpoint); err != nil {
		return nil, fmt.Errorf("fusefs: mountpoint %q: %w", mountpoint, err)
	} else if !fi.IsDir() {
		return nil, fmt.Errorf("fusefs: mountpoint %q is not a directory", mountpoint)
	}

	root := newRootNode(b, opt)

	ttl := cacheTTL
	mountOpts := fuse.MountOptions{
		FsName: "remolo",
		Name:   "remolo",
	}
	if opt.ReadOnly {
		mountOpts.Options = append(mountOpts.Options, "ro")
	}
	options := &gofs.Options{
		EntryTimeout: &ttl,
		AttrTimeout:  &ttl,
		MountOptions: mountOpts,
	}

	srv, err := gofs.Mount(mountpoint, root, options)
	if err != nil {
		return nil, fmt.Errorf("fusefs: mount %q: %w", mountpoint, err)
	}
	return &Server{impl: &fuseServer{srv: srv}}, nil
}

// IsUnavailable reports whether err indicates that FUSE mounting is
// impossible in the current environment (no /dev/fuse, missing
// fusermount helper, or insufficient privileges), as opposed to a
// genuine configuration mistake. Tests use it to t.Skip instead of
// failing on sandboxed CI.
func IsUnavailable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrUnsupportedPlatform) {
		return true
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		switch errno {
		case syscall.EPERM, syscall.EACCES, syscall.ENOENT, syscall.ENODEV, syscall.ENOSYS:
			return true
		}
	}
	if errors.Is(err, os.ErrPermission) || errors.Is(err, os.ErrNotExist) {
		return true
	}
	// The fusermount helper failures surface as plain text; match the
	// common phrasings rather than panicking on an unmountable host.
	msg := strings.ToLower(err.Error())
	for _, sub := range []string{
		"fusermount",
		"permission denied",
		"operation not permitted",
		"no such file or directory",
		"/dev/fuse",
		"not found",
	} {
		if strings.Contains(msg, sub) {
			return true
		}
	}
	return false
}
