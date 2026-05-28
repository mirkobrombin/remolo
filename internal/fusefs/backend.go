// Package fusefs exposes a remote host's files as a local FUSE
// filesystem. All actual I/O is delegated to an injected Backend, so
// this package has no dependency on remolo's session, rpc or
// filetransfer packages: the integrator wires a Backend implementation
// to the remolo connection (RPC for metadata, file channels for data).
//
// The point is that local editors and IDEs open remote files as if
// they were local, while the I/O rides the remolo connection and
// therefore keeps working behind NAT.
package fusefs

import "errors"

// ErrUnsupportedPlatform is returned by Mount when the current OS does
// not support FUSE mounts (anything other than linux or darwin), so the
// caller can surface a clear message instead of panicking.
var ErrUnsupportedPlatform = errors.New("fusefs: FUSE mounts are only supported on linux and darwin")

// ErrReadOnly is returned by Backend methods (and surfaced as EROFS by
// the FUSE layer) when a mutating operation is attempted on a read-only
// mount.
var ErrReadOnly = errors.New("fusefs: filesystem is read-only")

// FileInfo describes a single filesystem entry. It is intentionally
// minimal and transport friendly so it can be carried over remolo's RPC
// channel without pulling in os.FileInfo.
type FileInfo struct {
	// Name is the basename of the entry (not a full path).
	Name string
	// Size is the file size in bytes.
	Size int64
	// Mode is the raw unix mode bits, including the file-type bits
	// (for example syscall.S_IFREG or syscall.S_IFDIR). If the type
	// bits are absent, IsDir is used to infer directory vs regular
	// file.
	Mode uint32
	// IsDir reports whether the entry is a directory.
	IsDir bool
	// ModTime is the modification time as a unix timestamp (seconds).
	ModTime int64
}

// Backend is the set of operations the FUSE layer delegates to. The
// integrator implements it on top of the remolo connection:
//
//   - List/Stat map to the host-side RPC (ls/stat).
//   - ReadAt/WriteAt/Truncate map to ranged file-transfer or a
//     dedicated pread/pwrite/truncate RPC.
//   - Create/Mkdir/Remove/Rename map to the corresponding RPC verbs.
//
// All paths are absolute, slash-separated, and rooted at the host's
// exported root (the root itself is "/"). Implementations must be safe
// for concurrent use: the kernel can issue many FUSE requests in
// parallel.
type Backend interface {
	// List returns the entries of the directory at path.
	List(path string) ([]FileInfo, error)
	// Stat returns metadata for a single entry.
	Stat(path string) (FileInfo, error)
	// ReadAt reads len(p) bytes starting at offset off into p. It
	// returns the number of bytes read; a short read (n < len(p)) at
	// or past end-of-file is not an error and should return n with a
	// nil error (or io.EOF, which the FUSE layer treats as success).
	ReadAt(path string, off int64, p []byte) (int, error)
	// WriteAt writes p starting at offset off and returns the number
	// of bytes written. On a read-only backend it should return
	// ErrReadOnly.
	WriteAt(path string, off int64, p []byte) (int, error)
	// Truncate changes the size of the file at path.
	Truncate(path string, size int64) error
	// Create creates a new, empty regular file with the given mode.
	Create(path string, mode uint32) error
	// Mkdir creates a new directory with the given mode.
	Mkdir(path string, mode uint32) error
	// Remove removes the file or (empty) directory at path.
	Remove(path string) error
	// Rename moves oldp to newp.
	Rename(oldp, newp string) error
}

// Options tunes the behavior of a mount.
type Options struct {
	// ReadOnly, when true, makes the FUSE layer reject every mutating
	// operation with EROFS without ever calling the backend's write
	// paths.
	ReadOnly bool
}
