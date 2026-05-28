//go:build linux || darwin

package fusefs

import (
	"context"
	"io"
	"path"
	"sync"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

// cacheTTL is the lifetime of a cached Stat / small-read entry. It is
// deliberately short: it smooths bursts of redundant kernel calls (for
// example a stat storm from an editor) without letting the local view
// drift far from the host.
const cacheTTL = 1 * time.Second

// smallReadMax is the largest read that is eligible for the read cache.
// Editors and IDEs typically probe the head of a file repeatedly; we
// only cache those small head reads to stay memory-cheap.
const smallReadMax = 64 * 1024

// node is a single inode in the remote filesystem tree. Every node
// delegates its real work to the shared *root.backend. The node only
// remembers its own absolute path within the exported tree.
type node struct {
	fs.Inode

	root *root
	// path is the absolute, slash-separated path of this node inside
	// the exported tree. The mount point itself is "/".
	path string

	mu        sync.Mutex
	attr      FileInfo
	attrAt    time.Time
	attrValid bool

	readBuf []byte
	readOff int64
	readAt  time.Time
}

// root holds state shared by every node in a single mount.
type root struct {
	backend Backend
	opt     Options
}

// Interface assertions: these document (and compile-check) exactly which
// FUSE operations the node implements.
var (
	_ = (fs.NodeLookuper)((*node)(nil))
	_ = (fs.NodeReaddirer)((*node)(nil))
	_ = (fs.NodeGetattrer)((*node)(nil))
	_ = (fs.NodeSetattrer)((*node)(nil))
	_ = (fs.NodeOpener)((*node)(nil))
	_ = (fs.NodeReader)((*node)(nil))
	_ = (fs.NodeWriter)((*node)(nil))
	_ = (fs.NodeCreater)((*node)(nil))
	_ = (fs.NodeMkdirer)((*node)(nil))
	_ = (fs.NodeUnlinker)((*node)(nil))
	_ = (fs.NodeRmdirer)((*node)(nil))
	_ = (fs.NodeRenamer)((*node)(nil))
)

// newRootNode builds the inode embedder for the mount point.
func newRootNode(b Backend, opt Options) *node {
	return &node{
		root: &root{backend: b, opt: opt},
		path: "/",
	}
}

// childPath joins the node's path with a child name.
func (n *node) childPath(name string) string {
	return path.Join(n.path, name)
}

// modeBits returns the full unix mode for a FileInfo, synthesizing the
// file-type bits from IsDir when the backend did not supply them.
func modeBits(fi FileInfo) uint32 {
	m := fi.Mode
	if m&syscall.S_IFMT == 0 {
		if fi.IsDir {
			m |= syscall.S_IFDIR
			if m&0o777 == 0 {
				m |= 0o755
			}
		} else {
			m |= syscall.S_IFREG
			if m&0o777 == 0 {
				m |= 0o644
			}
		}
	}
	return m
}

// fillAttr copies a FileInfo into a fuse.Attr.
func fillAttr(a *fuse.Attr, fi FileInfo) {
	a.Mode = modeBits(fi)
	if fi.IsDir || a.Mode&syscall.S_IFDIR != 0 {
		a.Size = 4096
	} else {
		a.Size = uint64(fi.Size)
	}
	if fi.ModTime > 0 {
		a.Mtime = uint64(fi.ModTime)
		a.Ctime = uint64(fi.ModTime)
		a.Atime = uint64(fi.ModTime)
	}
}

// stableAttr derives the StableAttr (used for the inode type) from a
// FileInfo.
func stableAttr(fi FileInfo) fs.StableAttr {
	return fs.StableAttr{Mode: modeBits(fi) & syscall.S_IFMT}
}

// statCached returns the node's metadata, using a short-lived cache.
func (n *node) statCached() (FileInfo, syscall.Errno) {
	n.mu.Lock()
	if n.attrValid && time.Since(n.attrAt) < cacheTTL {
		fi := n.attr
		n.mu.Unlock()
		return fi, 0
	}
	n.mu.Unlock()

	fi, err := n.root.backend.Stat(n.path)
	if err != nil {
		return FileInfo{}, errnoFromErr(err)
	}

	n.mu.Lock()
	n.attr = fi
	n.attrAt = time.Now()
	n.attrValid = true
	n.mu.Unlock()
	return fi, 0
}

// invalidateAttr drops the cached metadata for this node.
func (n *node) invalidateAttr() {
	n.mu.Lock()
	n.attrValid = false
	n.readBuf = nil
	n.mu.Unlock()
}

// Lookup resolves a child by name.
func (n *node) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	cp := n.childPath(name)
	fi, err := n.root.backend.Stat(cp)
	if err != nil {
		return nil, errnoFromErr(err)
	}
	fillAttr(&out.Attr, fi)
	out.SetEntryTimeout(cacheTTL)
	out.SetAttrTimeout(cacheTTL)

	child := &node{root: n.root, path: cp}
	child.mu.Lock()
	child.attr = fi
	child.attrAt = time.Now()
	child.attrValid = true
	child.mu.Unlock()

	return n.NewInode(ctx, child, stableAttr(fi)), 0
}

// Readdir lists the directory entries.
func (n *node) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries, err := n.root.backend.List(n.path)
	if err != nil {
		return nil, errnoFromErr(err)
	}
	list := make([]fuse.DirEntry, 0, len(entries))
	for _, e := range entries {
		if e.Name == "" || e.Name == "." || e.Name == ".." {
			continue
		}
		list = append(list, fuse.DirEntry{
			Name: e.Name,
			Mode: modeBits(e),
		})
	}
	return fs.NewListDirStream(list), 0
}

// Getattr returns the node's attributes.
func (n *node) Getattr(ctx context.Context, f fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	fi, errno := n.statCached()
	if errno != 0 {
		return errno
	}
	fillAttr(&out.Attr, fi)
	out.SetTimeout(cacheTTL)
	return 0
}

// Setattr handles truncation (and silently accepts mode/time changes so
// editors that chmod after save do not fail). Only size changes are
// pushed to the backend.
func (n *node) Setattr(ctx context.Context, f fs.FileHandle, in *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	if sz, ok := in.GetSize(); ok {
		if n.root.opt.ReadOnly {
			return syscall.EROFS
		}
		if err := n.root.backend.Truncate(n.path, int64(sz)); err != nil {
			return errnoFromErr(err)
		}
		n.invalidateAttr()
	}
	fi, errno := n.statCached()
	if errno != 0 {
		return errno
	}
	fillAttr(&out.Attr, fi)
	out.SetTimeout(cacheTTL)
	return 0
}

// Open checks read-only enforcement for write opens. There is no
// per-handle state: reads and writes are stateless ranged operations.
func (n *node) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	if n.root.opt.ReadOnly && isWriteFlags(flags) {
		return nil, 0, syscall.EROFS
	}
	return nil, 0, 0
}

// Read performs a ranged read through the backend, with a small head
// cache for responsiveness.
func (n *node) Read(ctx context.Context, f fs.FileHandle, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	if cached, ok := n.cachedRead(off, len(dest)); ok {
		nn := copy(dest, cached)
		return fuse.ReadResultData(dest[:nn]), 0
	}

	nread, err := n.root.backend.ReadAt(n.path, off, dest)
	if err != nil && err != io.EOF && nread == 0 {
		return nil, errnoFromErr(err)
	}

	if off == 0 && nread > 0 && nread <= smallReadMax {
		buf := make([]byte, nread)
		copy(buf, dest[:nread])
		n.mu.Lock()
		n.readBuf = buf
		n.readOff = off
		n.readAt = time.Now()
		n.mu.Unlock()
	}
	return fuse.ReadResultData(dest[:nread]), 0
}

// cachedRead returns a cached slice satisfying the [off, off+length)
// request, if a fresh cache entry fully covers it.
func (n *node) cachedRead(off int64, length int) ([]byte, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.readBuf == nil || time.Since(n.readAt) >= cacheTTL {
		return nil, false
	}
	start := off - n.readOff
	if start < 0 || start >= int64(len(n.readBuf)) {
		return nil, false
	}
	end := start + int64(length)
	if end > int64(len(n.readBuf)) {
		end = int64(len(n.readBuf))
	}
	return n.readBuf[start:end], true
}

// Write performs a ranged write through the backend.
func (n *node) Write(ctx context.Context, f fs.FileHandle, data []byte, off int64) (uint32, syscall.Errno) {
	if n.root.opt.ReadOnly {
		return 0, syscall.EROFS
	}
	nw, err := n.root.backend.WriteAt(n.path, off, data)
	if err != nil {
		return uint32(nw), errnoFromErr(err)
	}
	n.invalidateAttr()
	return uint32(nw), 0
}

// Create makes a new regular file and returns its inode.
func (n *node) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	if n.root.opt.ReadOnly {
		return nil, nil, 0, syscall.EROFS
	}
	cp := n.childPath(name)
	if err := n.root.backend.Create(cp, mode); err != nil {
		return nil, nil, 0, errnoFromErr(err)
	}
	fi, err := n.root.backend.Stat(cp)
	if err != nil {
		// Fall back to a synthetic attr if the backend cannot stat a
		// just-created file.
		fi = FileInfo{Name: name, Mode: mode | syscall.S_IFREG}
	}
	fillAttr(&out.Attr, fi)
	out.SetEntryTimeout(cacheTTL)
	out.SetAttrTimeout(cacheTTL)

	child := &node{root: n.root, path: cp}
	return n.NewInode(ctx, child, stableAttr(fi)), nil, 0, 0
}

// Mkdir creates a directory and returns its inode.
func (n *node) Mkdir(ctx context.Context, name string, mode uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if n.root.opt.ReadOnly {
		return nil, syscall.EROFS
	}
	cp := n.childPath(name)
	if err := n.root.backend.Mkdir(cp, mode); err != nil {
		return nil, errnoFromErr(err)
	}
	fi, err := n.root.backend.Stat(cp)
	if err != nil {
		fi = FileInfo{Name: name, Mode: mode | syscall.S_IFDIR, IsDir: true}
	}
	fillAttr(&out.Attr, fi)
	out.SetEntryTimeout(cacheTTL)
	out.SetAttrTimeout(cacheTTL)

	child := &node{root: n.root, path: cp}
	return n.NewInode(ctx, child, stableAttr(fi)), 0
}

// Unlink removes a file child.
func (n *node) Unlink(ctx context.Context, name string) syscall.Errno {
	if n.root.opt.ReadOnly {
		return syscall.EROFS
	}
	if err := n.root.backend.Remove(n.childPath(name)); err != nil {
		return errnoFromErr(err)
	}
	return 0
}

// Rmdir removes a directory child.
func (n *node) Rmdir(ctx context.Context, name string) syscall.Errno {
	if n.root.opt.ReadOnly {
		return syscall.EROFS
	}
	if err := n.root.backend.Remove(n.childPath(name)); err != nil {
		return errnoFromErr(err)
	}
	return 0
}

// Rename moves a child to a (possibly different) parent directory.
func (n *node) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	if n.root.opt.ReadOnly {
		return syscall.EROFS
	}
	np, ok := newParent.(*node)
	if !ok {
		return syscall.EINVAL
	}
	oldp := n.childPath(name)
	newp := np.childPath(newName)
	if err := n.root.backend.Rename(oldp, newp); err != nil {
		return errnoFromErr(err)
	}
	return 0
}

// isWriteFlags reports whether the open flags request write access.
func isWriteFlags(flags uint32) bool {
	acc := flags & uint32(syscall.O_ACCMODE)
	return acc == uint32(syscall.O_WRONLY) || acc == uint32(syscall.O_RDWR) ||
		flags&uint32(syscall.O_TRUNC) != 0 || flags&uint32(syscall.O_APPEND) != 0
}
