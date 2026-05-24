package dirsync

import (
	"bytes"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"testing"
)

// serveOnPipe runs Serve in a goroutine against one end of a net.Pipe and
// returns the client end plus a channel carrying Serve's error.
func serveOnPipe(t *testing.T, root string) (net.Conn, <-chan error) {
	t.Helper()
	c1, c2 := net.Pipe()
	errc := make(chan error, 1)
	go func() {
		errc <- Serve(c2, root)
		c2.Close()
	}()
	return c1, errc
}

// buildTree writes a small but varied tree under root: nested dirs, files of
// several sizes, and a symlink.
func buildTree(t *testing.T, root string) {
	t.Helper()
	mk := func(rel string, data []byte) {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("readme.txt", []byte("hello world"))
	mk("a/small.bin", bytes.Repeat([]byte("x"), 100))
	mk("a/b/medium.dat", randBytes(t, 50*1024))
	mk("a/b/c/deep.txt", []byte("deep file\n"))
	mk("big.bin", randBytes(t, 512*1024))

	// An executable file to verify mode preservation.
	exe := filepath.Join(root, "run.sh")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// A symlink pointing at readme.txt.
	link := filepath.Join(root, "a", "link-to-readme")
	if err := os.Symlink("../readme.txt", link); err != nil {
		t.Fatal(err)
	}
}

func randBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// countTreeFiles counts regular files (not dirs, not symlinks) under root.
func countTreeFiles(t *testing.T, root string) int {
	t.Helper()
	n := 0
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// assertTreesEqual checks that every entry in src appears in dst with identical
// content/mode (for files) or target (for symlinks).
func assertTreesEqual(t *testing.T, src, dst string) {
	t.Helper()
	ex := NewExcluder(nil)
	srcMan, err := BuildManifest(src, ex, true)
	if err != nil {
		t.Fatal(err)
	}
	dstMan, err := BuildManifest(dst, ex, true)
	if err != nil {
		t.Fatal(err)
	}
	for p, se := range srcMan {
		de, ok := dstMan[p]
		if !ok {
			t.Fatalf("dst missing %q", p)
		}
		if se.Kind != de.Kind {
			t.Fatalf("%q kind mismatch: %v vs %v", p, se.Kind, de.Kind)
		}
		switch se.Kind {
		case KindFile:
			if se.Sha256 != de.Sha256 {
				t.Fatalf("%q content mismatch", p)
			}
			if se.Mode.Perm() != de.Mode.Perm() {
				t.Fatalf("%q mode mismatch: %v vs %v", p, se.Mode, de.Mode)
			}
		case KindSymlink:
			if se.Target != de.Target {
				t.Fatalf("%q symlink target mismatch: %q vs %q", p, se.Target, de.Target)
			}
		}
	}
}

func TestPushFreshAndNoop(t *testing.T) {
	src := t.TempDir()
	dstRoot := t.TempDir()
	buildTree(t, src)

	fileCount := countTreeFiles(t, src)

	// First push: everything transfers.
	conn, errc := serveOnPipe(t, dstRoot)
	stats, err := Push(conn, src, "tree", Options{})
	conn.Close()
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if serveErr := <-errc; serveErr != nil {
		t.Fatalf("serve: %v", serveErr)
	}
	dst := filepath.Join(dstRoot, "tree")
	assertTreesEqual(t, src, dst)
	if stats.FilesTransferred != fileCount {
		t.Fatalf("first push transferred %d, want %d", stats.FilesTransferred, fileCount)
	}

	// Second push of the unchanged tree: no-op.
	conn2, errc2 := serveOnPipe(t, dstRoot)
	stats2, err := Push(conn2, src, "tree", Options{})
	conn2.Close()
	if err != nil {
		t.Fatalf("push2: %v", err)
	}
	if serveErr := <-errc2; serveErr != nil {
		t.Fatalf("serve2: %v", serveErr)
	}
	if stats2.FilesTransferred != 0 {
		t.Fatalf("second push transferred %d, want 0", stats2.FilesTransferred)
	}
	assertTreesEqual(t, src, dst)
}

func TestPushBlockDelta(t *testing.T) {
	src := t.TempDir()
	dstRoot := t.TempDir()
	buildTree(t, src)

	// Initial push.
	conn, errc := serveOnPipe(t, dstRoot)
	if _, err := Push(conn, src, "tree", Options{}); err != nil {
		t.Fatalf("push: %v", err)
	}
	conn.Close()
	if err := <-errc; err != nil {
		t.Fatalf("serve: %v", err)
	}

	// Edit one byte in the big file.
	bigPath := filepath.Join(src, "big.bin")
	data, err := os.ReadFile(bigPath)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)/2] ^= 0xFF
	if err := os.WriteFile(bigPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	// Re-push: only big.bin should move, and far fewer bytes than its size.
	conn2, errc2 := serveOnPipe(t, dstRoot)
	stats, err := Push(conn2, src, "tree", Options{})
	conn2.Close()
	if err != nil {
		t.Fatalf("push2: %v", err)
	}
	if err := <-errc2; err != nil {
		t.Fatalf("serve2: %v", err)
	}
	if stats.FilesTransferred != 1 {
		t.Fatalf("transferred %d files, want 1", stats.FilesTransferred)
	}
	if stats.BytesTransferred >= int64(len(data)/2) {
		t.Fatalf("transferred %d bytes for a 1-byte edit in a %d-byte file; block delta not effective",
			stats.BytesTransferred, len(data))
	}
	assertTreesEqual(t, src, filepath.Join(dstRoot, "tree"))
}

func TestPull(t *testing.T) {
	remoteRoot := t.TempDir()
	local := t.TempDir()
	// The host's served tree lives under remoteRoot/tree.
	srcTree := filepath.Join(remoteRoot, "tree")
	if err := os.MkdirAll(srcTree, 0o755); err != nil {
		t.Fatal(err)
	}
	buildTree(t, srcTree)
	fileCount := countTreeFiles(t, srcTree)

	conn, errc := serveOnPipe(t, remoteRoot)
	stats, err := Pull(conn, "tree", local, Options{})
	conn.Close()
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if serveErr := <-errc; serveErr != nil {
		t.Fatalf("serve: %v", serveErr)
	}
	if stats.FilesTransferred != fileCount {
		t.Fatalf("pull transferred %d, want %d", stats.FilesTransferred, fileCount)
	}
	assertTreesEqual(t, srcTree, local)
}

func TestDelete(t *testing.T) {
	src := t.TempDir()
	dstRoot := t.TempDir()
	buildTree(t, src)

	// Push once.
	conn, errc := serveOnPipe(t, dstRoot)
	if _, err := Push(conn, src, "tree", Options{}); err != nil {
		t.Fatalf("push: %v", err)
	}
	conn.Close()
	if err := <-errc; err != nil {
		t.Fatalf("serve: %v", err)
	}
	dst := filepath.Join(dstRoot, "tree")

	// Add an extra file on the receiver side.
	extra := filepath.Join(dst, "extra-file.txt")
	if err := os.WriteFile(extra, []byte("orphan"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Push without --delete: the extra survives.
	conn2, errc2 := serveOnPipe(t, dstRoot)
	if _, err := Push(conn2, src, "tree", Options{}); err != nil {
		t.Fatalf("push2: %v", err)
	}
	conn2.Close()
	<-errc2
	if _, err := os.Stat(extra); err != nil {
		t.Fatalf("extra removed without --delete: %v", err)
	}

	// Push with --delete: the extra is removed.
	conn3, errc3 := serveOnPipe(t, dstRoot)
	if _, err := Push(conn3, src, "tree", Options{Delete: true}); err != nil {
		t.Fatalf("push3: %v", err)
	}
	conn3.Close()
	if err := <-errc3; err != nil {
		t.Fatalf("serve3: %v", err)
	}
	if _, err := os.Stat(extra); !os.IsNotExist(err) {
		t.Fatalf("extra still present after --delete (err=%v)", err)
	}
	assertTreesEqual(t, src, dst)
}

func TestDryRun(t *testing.T) {
	src := t.TempDir()
	dstRoot := t.TempDir()
	buildTree(t, src)
	dst := filepath.Join(dstRoot, "tree")

	conn, errc := serveOnPipe(t, dstRoot)
	stats, err := Push(conn, src, "tree", Options{DryRun: true})
	conn.Close()
	if err != nil {
		t.Fatalf("dry-run push: %v", err)
	}
	if err := <-errc; err != nil {
		t.Fatalf("serve: %v", err)
	}
	if stats.FilesTransferred == 0 {
		t.Fatalf("dry-run reported 0 files transferred; expected the count it would move")
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("dry-run created the destination tree (err=%v)", err)
	}
}

func TestExcludes(t *testing.T) {
	src := t.TempDir()
	dstRoot := t.TempDir()
	buildTree(t, src)
	// Add files that should be excluded.
	if err := os.MkdirAll(filepath.Join(src, "node_modules", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "node_modules", "pkg", "index.js"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a", "temp.log"), []byte("log"), 0o644); err != nil {
		t.Fatal(err)
	}

	conn, errc := serveOnPipe(t, dstRoot)
	_, err := Push(conn, src, "tree", Options{Excludes: []string{"node_modules", "*.log"}})
	conn.Close()
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if err := <-errc; err != nil {
		t.Fatalf("serve: %v", err)
	}
	dst := filepath.Join(dstRoot, "tree")
	if _, err := os.Stat(filepath.Join(dst, "node_modules")); !os.IsNotExist(err) {
		t.Fatalf("excluded node_modules present (err=%v)", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "a", "temp.log")); !os.IsNotExist(err) {
		t.Fatalf("excluded *.log present (err=%v)", err)
	}
	// Non-excluded content still arrived.
	if _, err := os.Stat(filepath.Join(dst, "readme.txt")); err != nil {
		t.Fatalf("non-excluded file missing: %v", err)
	}
}

func TestResolveWithinRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	bad := []string{"../escape", "a/../../escape", "/etc/passwd"}
	for _, b := range bad {
		if _, err := resolveWithin(root, b); err == nil {
			t.Fatalf("resolveWithin allowed traversal %q", b)
		}
	}
	good := []string{"", "sub", "a/b/c"}
	for _, g := range good {
		if _, err := resolveWithin(root, g); err != nil {
			t.Fatalf("resolveWithin rejected valid %q: %v", g, err)
		}
	}
}
