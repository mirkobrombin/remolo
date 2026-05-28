package fusefs

import (
	"errors"
	"io/fs"
	"syscall"
)

// errnoFromErr translates a backend error into a FUSE errno. The kernel
// only understands errno values, so every backend failure must be
// reduced to one. Recognized cases map to their natural errno; anything
// else becomes EIO.
func errnoFromErr(err error) syscall.Errno {
	if err == nil {
		return 0
	}

	// Allow backends to return a syscall.Errno directly.
	var e syscall.Errno
	if errors.As(err, &e) {
		return e
	}

	switch {
	case errors.Is(err, ErrReadOnly):
		return syscall.EROFS
	case errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOENT):
		return syscall.ENOENT
	case errors.Is(err, fs.ErrExist) || errors.Is(err, syscall.EEXIST):
		return syscall.EEXIST
	case errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EACCES):
		return syscall.EACCES
	case errors.Is(err, syscall.ENOTEMPTY):
		return syscall.ENOTEMPTY
	case errors.Is(err, syscall.EISDIR):
		return syscall.EISDIR
	case errors.Is(err, syscall.ENOTDIR):
		return syscall.ENOTDIR
	case errors.Is(err, syscall.EINVAL):
		return syscall.EINVAL
	default:
		return syscall.EIO
	}
}
