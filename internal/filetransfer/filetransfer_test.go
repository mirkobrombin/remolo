package filetransfer

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mirkobrombin/remolo/internal/protocol"
)

// pipePair returns two ends of an in-memory connection for driving a client
// against a server goroutine.
func pipePair() (net.Conn, net.Conn) { return net.Pipe() }

func writeTemp(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	return p
}

func sha(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

func TestPutThenGet(t *testing.T) {
	srcDir := t.TempDir()
	rootDir := t.TempDir()
	dstDir := t.TempDir()

	payload := make([]byte, 200*1024+123) // spans many 32 KiB chunks
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("rand: %v", err)
	}
	localPath := writeTemp(t, srcDir, "src.bin", payload)

	// --- PUT: client uploads local file into rootDir as "uploaded.bin".
	c, s := pipePair()
	srvErr := make(chan error, 1)
	go func() { srvErr <- Serve(s, rootDir) }()

	if err := Put(c, localPath, "uploaded.bin"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := <-srvErr; err != nil {
		t.Fatalf("Serve(put): %v", err)
	}
	c.Close()

	got, err := os.ReadFile(filepath.Join(rootDir, "uploaded.bin"))
	if err != nil {
		t.Fatalf("read uploaded: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("uploaded content mismatch: got %d bytes want %d", len(got), len(payload))
	}
	if sha(got) != sha(payload) {
		t.Fatalf("uploaded sha mismatch")
	}

	// --- GET: client downloads it back into dstDir.
	c2, s2 := pipePair()
	srvErr2 := make(chan error, 1)
	go func() { srvErr2 <- Serve(s2, rootDir) }()

	dst := filepath.Join(dstDir, "downloaded.bin")
	n, err := Get(c2, "uploaded.bin", dst)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if err := <-srvErr2; err != nil {
		t.Fatalf("Serve(get): %v", err)
	}
	c2.Close()

	if n != int64(len(payload)) {
		t.Fatalf("Get returned %d, want %d", n, len(payload))
	}
	back, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read downloaded: %v", err)
	}
	if !bytes.Equal(back, payload) {
		t.Fatalf("downloaded content mismatch")
	}
	if sha(back) != sha(payload) {
		t.Fatalf("downloaded sha mismatch")
	}
}

func TestGetResume(t *testing.T) {
	rootDir := t.TempDir()
	dstDir := t.TempDir()

	payload := make([]byte, 100*1024)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("rand: %v", err)
	}
	writeTemp(t, rootDir, "file.bin", payload)

	// Pre-write a partial destination (first 40 KiB) to exercise resume.
	partial := 40 * 1024
	dst := filepath.Join(dstDir, "file.bin")
	if err := os.WriteFile(dst, payload[:partial], 0o644); err != nil {
		t.Fatalf("write partial: %v", err)
	}

	c, s := pipePair()
	srvErr := make(chan error, 1)
	go func() { srvErr <- Serve(s, rootDir) }()

	n, err := Get(c, "file.bin", dst)
	if err != nil {
		t.Fatalf("Get(resume): %v", err)
	}
	if err := <-srvErr; err != nil {
		t.Fatalf("Serve(get resume): %v", err)
	}
	c.Close()

	if n != int64(len(payload)) {
		t.Fatalf("resume total = %d, want %d", n, len(payload))
	}
	back, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read resumed: %v", err)
	}
	if !bytes.Equal(back, payload) {
		t.Fatalf("resumed content mismatch")
	}
}

func TestPutResume(t *testing.T) {
	srcDir := t.TempDir()
	rootDir := t.TempDir()

	payload := make([]byte, 80*1024)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("rand: %v", err)
	}
	localPath := writeTemp(t, srcDir, "src.bin", payload)

	c, s := pipePair()
	srvErr := make(chan error, 1)
	go func() { srvErr <- Serve(s, rootDir) }()

	if err := Put(c, localPath, "dst.bin"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := <-srvErr; err != nil {
		t.Fatalf("Serve(put): %v", err)
	}
	c.Close()

	out, err := os.ReadFile(filepath.Join(rootDir, "dst.bin"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(out, payload) {
		t.Fatalf("content mismatch")
	}
}

func TestPathTraversalRejected(t *testing.T) {
	rootDir := t.TempDir()

	c, s := pipePair()
	srvErr := make(chan error, 1)
	go func() { srvErr <- Serve(s, rootDir) }()

	// Try to read outside root via a get with a traversal path.
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	dst := filepath.Join(t.TempDir(), "stolen")
	_, err := Get(c, "../../../etc/passwd", dst)
	if err == nil {
		t.Fatalf("expected traversal to be rejected")
	}
	if serr := <-srvErr; serr == nil {
		t.Fatalf("expected server to reject traversal")
	}
	c.Close()
}

func TestShaMismatchReported(t *testing.T) {
	rootDir := t.TempDir()

	c, s := pipePair()
	srvErr := make(chan error, 1)
	go func() {
		srvErr <- servePut(s, filepath.Join(rootDir, "x.bin"),
			Request{Op: "put", Sha256: sha([]byte("not the real content"))})
	}()

	// Send a put body directly with content that will not match the sha:
	// data frame then EOF.
	data := []byte("actual content")
	if err := protocol.WriteFrame(c, protocol.FrameData, data); err != nil {
		t.Fatalf("write data: %v", err)
	}
	if err := protocol.WriteFrame(c, protocol.FrameEOF, nil); err != nil {
		t.Fatalf("write eof: %v", err)
	}
	if err := readAck(c); err == nil {
		t.Fatalf("expected sha mismatch ack error")
	}
	if serr := <-srvErr; serr == nil {
		t.Fatalf("expected server to report sha mismatch")
	}
	c.Close()
}
