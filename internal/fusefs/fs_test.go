//go:build linux || darwin

package fusefs

import (
	"context"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/hanwen/go-fuse/v2/fuse"
)

// memFile is one entry in the in-memory fake filesystem.
type memFile struct {
	data    []byte
	mode    uint32
	isDir   bool
	modTime int64
}

// memBackend is a map-backed fake Backend used to verify that the FUSE
// node and mount round-trip operations to a backend. Keys are absolute,
// clean, slash-separated paths; "/" is the root directory.
type memBackend struct {
	mu       sync.Mutex
	files    map[string]*memFile
	readOnly bool
}

func newMemBackend() *memBackend {
	return &memBackend{
		files: map[string]*memFile{
			"/": {isDir: true, mode: 0o755, modTime: time.Now().Unix()},
		},
	}
}

func clean(p string) string {
	if p == "" {
		return "/"
	}
	return path.Clean("/" + p)
}

func (m *memBackend) List(p string) ([]FileInfo, error) {
	p = clean(p)
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.files[p]
	if !ok {
		return nil, syscall.ENOENT
	}
	if !d.isDir {
		return nil, syscall.ENOTDIR
	}
	var out []FileInfo
	for fp, f := range m.files {
		if fp == p {
			continue
		}
		if path.Dir(fp) != p {
			continue
		}
		out = append(out, FileInfo{
			Name:    path.Base(fp),
			Size:    int64(len(f.data)),
			Mode:    f.mode,
			IsDir:   f.isDir,
			ModTime: f.modTime,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (m *memBackend) Stat(p string) (FileInfo, error) {
	p = clean(p)
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.files[p]
	if !ok {
		return FileInfo{}, syscall.ENOENT
	}
	return FileInfo{
		Name:    path.Base(p),
		Size:    int64(len(f.data)),
		Mode:    f.mode,
		IsDir:   f.isDir,
		ModTime: f.modTime,
	}, nil
}

func (m *memBackend) ReadAt(p string, off int64, b []byte) (int, error) {
	p = clean(p)
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.files[p]
	if !ok {
		return 0, syscall.ENOENT
	}
	if off >= int64(len(f.data)) {
		return 0, io.EOF
	}
	n := copy(b, f.data[off:])
	return n, nil
}

func (m *memBackend) WriteAt(p string, off int64, b []byte) (int, error) {
	p = clean(p)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.readOnly {
		return 0, ErrReadOnly
	}
	f, ok := m.files[p]
	if !ok {
		return 0, syscall.ENOENT
	}
	end := off + int64(len(b))
	if end > int64(len(f.data)) {
		grown := make([]byte, end)
		copy(grown, f.data)
		f.data = grown
	}
	copy(f.data[off:], b)
	f.modTime = time.Now().Unix()
	return len(b), nil
}

func (m *memBackend) Truncate(p string, size int64) error {
	p = clean(p)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.readOnly {
		return ErrReadOnly
	}
	f, ok := m.files[p]
	if !ok {
		return syscall.ENOENT
	}
	if size <= int64(len(f.data)) {
		f.data = f.data[:size]
	} else {
		grown := make([]byte, size)
		copy(grown, f.data)
		f.data = grown
	}
	return nil
}

func (m *memBackend) Create(p string, mode uint32) error {
	p = clean(p)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.readOnly {
		return ErrReadOnly
	}
	if _, ok := m.files[path.Dir(p)]; !ok {
		return syscall.ENOENT
	}
	if _, ok := m.files[p]; ok {
		return syscall.EEXIST
	}
	m.files[p] = &memFile{mode: mode &^ syscall.S_IFMT, modTime: time.Now().Unix()}
	return nil
}

func (m *memBackend) Mkdir(p string, mode uint32) error {
	p = clean(p)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.readOnly {
		return ErrReadOnly
	}
	if _, ok := m.files[path.Dir(p)]; !ok {
		return syscall.ENOENT
	}
	if _, ok := m.files[p]; ok {
		return syscall.EEXIST
	}
	m.files[p] = &memFile{isDir: true, mode: mode &^ syscall.S_IFMT, modTime: time.Now().Unix()}
	return nil
}

func (m *memBackend) Remove(p string) error {
	p = clean(p)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.readOnly {
		return ErrReadOnly
	}
	f, ok := m.files[p]
	if !ok {
		return syscall.ENOENT
	}
	if f.isDir {
		for fp := range m.files {
			if fp != p && path.Dir(fp) == p {
				return syscall.ENOTEMPTY
			}
		}
	}
	delete(m.files, p)
	return nil
}

func (m *memBackend) Rename(oldp, newp string) error {
	oldp, newp = clean(oldp), clean(newp)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.readOnly {
		return ErrReadOnly
	}
	f, ok := m.files[oldp]
	if !ok {
		return syscall.ENOENT
	}
	if _, ok := m.files[path.Dir(newp)]; !ok {
		return syscall.ENOENT
	}
	delete(m.files, oldp)
	m.files[newp] = f
	return nil
}

// helper to seed a regular file.
func (m *memBackend) put(p string, data []byte) {
	p = clean(p)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[p] = &memFile{data: data, mode: 0o644, modTime: time.Now().Unix()}
}

// --- Pure unit tests: exercise the node directly, no mount needed. ---

func TestNodeDelegation(t *testing.T) {
	b := newMemBackend()
	b.put("/hello.txt", []byte("hello world"))
	b.Mkdir("/sub", 0o755)
	b.put("/sub/inner.txt", []byte("inner"))

	root := newRootNode(b, Options{})
	ctx := context.Background()

	// Getattr on root reports a directory.
	var ao fuse.AttrOut
	if errno := root.Getattr(ctx, nil, &ao); errno != 0 {
		t.Fatalf("root Getattr: errno %v", errno)
	}
	if ao.Mode&syscall.S_IFDIR == 0 {
		t.Fatalf("root is not a dir: mode %o", ao.Mode)
	}

	// Readdir lists the two top-level entries.
	ds, errno := root.Readdir(ctx)
	if errno != 0 {
		t.Fatalf("Readdir: errno %v", errno)
	}
	var names []string
	for ds.HasNext() {
		de, e := ds.Next()
		if e != 0 {
			t.Fatalf("dirstream Next: %v", e)
		}
		names = append(names, de.Name)
	}
	ds.Close()
	sort.Strings(names)
	if len(names) != 2 || names[0] != "hello.txt" || names[1] != "sub" {
		t.Fatalf("unexpected dir entries: %v", names)
	}

	// statCached / read path through the node.
	fi, errno := root.statCached()
	if errno != 0 || !fi.IsDir {
		t.Fatalf("statCached root: %v isDir=%v", errno, fi.IsDir)
	}

	// Read a file by reaching through the backend the way Read does.
	dest := make([]byte, 32)
	fileNode := &node{root: root.root, path: "/hello.txt"}
	rr, errno := fileNode.Read(ctx, nil, dest, 0)
	if errno != 0 {
		t.Fatalf("Read: errno %v", errno)
	}
	got, st := rr.Bytes(make([]byte, 32))
	if st != fuse.OK {
		t.Fatalf("ReadResult.Bytes status %v", st)
	}
	if string(got) != "hello world" {
		t.Fatalf("read got %q", string(got))
	}

	// Write through the node.
	wn, errno := fileNode.Write(ctx, nil, []byte("HELLO"), 0)
	if errno != 0 || wn != 5 {
		t.Fatalf("Write: errno=%v n=%d", errno, wn)
	}
	if got, _ := b.Stat("/hello.txt"); got.Size != 11 {
		t.Fatalf("after write size = %d", got.Size)
	}
	rr2, _ := fileNode.Read(ctx, nil, make([]byte, 5), 0)
	b2, _ := rr2.Bytes(make([]byte, 5))
	if string(b2) != "HELLO" {
		t.Fatalf("post-write read = %q", string(b2))
	}
}

func TestNodeReadOnlyRejectsWrites(t *testing.T) {
	b := newMemBackend()
	b.readOnly = true
	b.put("/ro.txt", []byte("data"))

	root := newRootNode(b, Options{ReadOnly: true})
	ctx := context.Background()
	fn := &node{root: root.root, path: "/ro.txt"}

	if _, errno := fn.Write(ctx, nil, []byte("x"), 0); errno != syscall.EROFS {
		t.Fatalf("write on RO mount: errno %v, want EROFS", errno)
	}
	if errno := root.Unlink(ctx, "ro.txt"); errno != syscall.EROFS {
		t.Fatalf("unlink on RO mount: errno %v, want EROFS", errno)
	}
	if _, errno := root.Mkdir(ctx, "d", 0o755, &fuse.EntryOut{}); errno != syscall.EROFS {
		t.Fatalf("mkdir on RO mount: errno %v, want EROFS", errno)
	}
	var so fuse.SetAttrIn
	so.Valid = fuse.FATTR_SIZE
	so.Size = 0
	if errno := fn.Setattr(ctx, nil, &so, &fuse.AttrOut{}); errno != syscall.EROFS {
		t.Fatalf("truncate on RO mount: errno %v, want EROFS", errno)
	}
}

func TestErrnoMapping(t *testing.T) {
	cases := []struct {
		err  error
		want syscall.Errno
	}{
		{nil, 0},
		{syscall.ENOENT, syscall.ENOENT},
		{ErrReadOnly, syscall.EROFS},
		{os.ErrNotExist, syscall.ENOENT},
		{io.ErrUnexpectedEOF, syscall.EIO},
	}
	for _, c := range cases {
		if got := errnoFromErr(c.err); got != c.want {
			t.Errorf("errnoFromErr(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

// --- Integration test: a real FUSE mount, skipped when unavailable. ---

func TestMountRoundTrip(t *testing.T) {
	b := newMemBackend()
	b.put("/greeting.txt", []byte("hello from host"))
	b.Mkdir("/docs", 0o755)
	b.put("/docs/readme.md", []byte("# readme"))

	mnt := t.TempDir()
	srv, err := Mount(mnt, b, Options{})
	if err != nil {
		if IsUnavailable(err) {
			t.Skipf("FUSE unavailable in this environment, skipping mount test: %v", err)
		}
		t.Fatalf("Mount: %v", err)
	}
	defer func() {
		_ = srv.Unmount()
	}()

	// Readdir through the OS.
	ents, err := os.ReadDir(mnt)
	if err != nil {
		t.Fatalf("os.ReadDir: %v", err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	if len(names) != 2 || names[0] != "docs" || names[1] != "greeting.txt" {
		t.Fatalf("mount ReadDir got %v", names)
	}

	// ReadFile through the OS.
	got, err := os.ReadFile(filepath.Join(mnt, "greeting.txt"))
	if err != nil {
		t.Fatalf("os.ReadFile: %v", err)
	}
	if string(got) != "hello from host" {
		t.Fatalf("ReadFile got %q", string(got))
	}

	// Nested read.
	got, err = os.ReadFile(filepath.Join(mnt, "docs", "readme.md"))
	if err != nil || string(got) != "# readme" {
		t.Fatalf("nested ReadFile: %v %q", err, string(got))
	}

	// WriteFile (create + write) through the OS.
	if err := os.WriteFile(filepath.Join(mnt, "new.txt"), []byte("created"), 0o644); err != nil {
		t.Fatalf("os.WriteFile: %v", err)
	}
	if fi, _ := b.Stat("/new.txt"); fi.Size != int64(len("created")) {
		t.Fatalf("backend missing new.txt: size %d", fi.Size)
	}

	// Mkdir through the OS.
	if err := os.Mkdir(filepath.Join(mnt, "made"), 0o755); err != nil {
		t.Fatalf("os.Mkdir: %v", err)
	}
	if fi, err := b.Stat("/made"); err != nil || !fi.IsDir {
		t.Fatalf("backend missing made dir: %v", err)
	}

	// Rename through the OS.
	if err := os.Rename(filepath.Join(mnt, "new.txt"), filepath.Join(mnt, "renamed.txt")); err != nil {
		t.Fatalf("os.Rename: %v", err)
	}
	if _, err := b.Stat("/new.txt"); err == nil {
		t.Fatalf("old name still present after rename")
	}
	if _, err := b.Stat("/renamed.txt"); err != nil {
		t.Fatalf("renamed file missing: %v", err)
	}

	// Remove through the OS.
	if err := os.Remove(filepath.Join(mnt, "renamed.txt")); err != nil {
		t.Fatalf("os.Remove: %v", err)
	}
	if _, err := b.Stat("/renamed.txt"); err == nil {
		t.Fatalf("file still present after remove")
	}
}

func TestMountReadOnly(t *testing.T) {
	b := newMemBackend()
	b.put("/file.txt", []byte("readonly data"))

	mnt := t.TempDir()
	srv, err := Mount(mnt, b, Options{ReadOnly: true})
	if err != nil {
		if IsUnavailable(err) {
			t.Skipf("FUSE unavailable, skipping: %v", err)
		}
		t.Fatalf("Mount: %v", err)
	}
	defer func() { _ = srv.Unmount() }()

	got, err := os.ReadFile(filepath.Join(mnt, "file.txt"))
	if err != nil || string(got) != "readonly data" {
		t.Fatalf("read on RO mount: %v %q", err, string(got))
	}

	err = os.WriteFile(filepath.Join(mnt, "nope.txt"), []byte("x"), 0o644)
	if err == nil {
		t.Fatalf("expected write to fail on read-only mount")
	}
}
