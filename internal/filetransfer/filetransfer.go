// Package filetransfer implements resumable file transfer over a single remolo
// channel stream.
//
// The protocol is intentionally small. The initiating side sends a Request as
// the first FrameJSON frame, then bulk data flows as a sequence of FrameData
// chunks terminated by a FrameEOF. The receiving side replies with a FrameExit
// carrying the result of a sha256 verification (Code 0 on success, non-zero
// with Err set on failure).
//
// Two operations are supported:
//
//	"put": the client uploads localPath to remotePath on the server. The
//	       client streams data from Offset to the server, which appends it to
//	       the destination file under rootDir.
//	"get": the client downloads remotePath into localDest. The server streams
//	       the file from Offset; the client resumes by stat-ing localDest.
//
// Paths are always resolved against the server's rootDir and rejected if they
// escape it, so a peer cannot read or write outside the shared directory.
package filetransfer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mirkobrombin/remolo/internal/protocol"
)

// chunkSize is the size of a single FrameData chunk. 32 KiB stays well under
// protocol.MaxFramePayload while keeping per-frame overhead negligible.
const chunkSize = 32 * 1024

// Request is the first FrameJSON frame on a file-transfer stream. It tells the
// peer which operation to perform and where to resume from.
type Request struct {
	Op     string `json:"op"`     // "put" or "get"
	Path   string `json:"path"`   // path relative to the server's rootDir
	Offset int64  `json:"offset"` // byte offset to resume from
	Size   int64  `json:"size"`   // total size of the source file (informational)
	Sha256 string `json:"sha256"` // hex sha256 of the full source file
}

// Put uploads localPath to remotePath on the peer running Serve. It streams the
// local file as FrameData chunks starting at offset 0, sends FrameEOF, then
// waits for the server's FrameExit acknowledgement, which reports the result of
// the server-side sha256 verification.
func Put(stream io.ReadWriter, localPath, remotePath string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("filetransfer: open local: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("filetransfer: stat local: %w", err)
	}

	sum, err := fileSha256(localPath)
	if err != nil {
		return err
	}

	req := Request{
		Op:     "put",
		Path:   remotePath,
		Offset: 0,
		Size:   info.Size(),
		Sha256: sum,
	}
	if err := protocol.WriteJSON(stream, req); err != nil {
		return fmt.Errorf("filetransfer: send request: %w", err)
	}

	if err := streamData(stream, f); err != nil {
		return err
	}
	if err := protocol.WriteFrame(stream, protocol.FrameEOF, nil); err != nil {
		return fmt.Errorf("filetransfer: send eof: %w", err)
	}

	return readAck(stream)
}

// Get downloads remotePath from the peer running Serve into localDest. If
// localDest already exists it resumes from its current size. It returns the
// total number of bytes in the completed local file.
func Get(stream io.ReadWriter, remotePath, localDest string) (int64, error) {
	var offset int64
	if info, err := os.Stat(localDest); err == nil {
		offset = info.Size()
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, fmt.Errorf("filetransfer: stat dest: %w", err)
	}

	req := Request{Op: "get", Path: remotePath, Offset: offset}
	if err := protocol.WriteJSON(stream, req); err != nil {
		return 0, fmt.Errorf("filetransfer: send request: %w", err)
	}

	dst, err := os.OpenFile(localDest, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, fmt.Errorf("filetransfer: open dest: %w", err)
	}
	defer dst.Close()
	if _, err := dst.Seek(offset, io.SeekStart); err != nil {
		return 0, fmt.Errorf("filetransfer: seek dest: %w", err)
	}

	written, err := recvData(stream, dst)
	if err != nil {
		return 0, err
	}

	total := offset + written
	if err := dst.Sync(); err != nil {
		return 0, fmt.Errorf("filetransfer: sync dest: %w", err)
	}

	// The server sends a FrameExit ack carrying the expected sha of the full
	// file. Verify the completed local file against it.
	if err := readAck(stream); err != nil {
		return 0, err
	}
	return total, nil
}

// Serve handles a single file-transfer stream against rootDir. It reads the
// Request and dispatches to put or get handling. rootDir bounds every path.
func Serve(stream io.ReadWriter, rootDir string) error {
	return ServeWithPolicy(stream, rootDir, true)
}

// ServeWithPolicy is Serve with an explicit write policy: when allowWrite is
// false, "put" (upload) requests are refused (read-only file access).
func ServeWithPolicy(stream io.ReadWriter, rootDir string, allowWrite bool) error {
	ft, payload, err := protocol.ReadFrame(stream)
	if err != nil {
		return fmt.Errorf("filetransfer: read request: %w", err)
	}
	if ft != protocol.FrameJSON {
		return fmt.Errorf("filetransfer: expected request frame, got type %d", ft)
	}
	var req Request
	if err := json.Unmarshal(payload, &req); err != nil {
		return fmt.Errorf("filetransfer: decode request: %w", err)
	}

	abs, err := resolve(rootDir, req.Path)
	if err != nil {
		// Report the rejection to the peer where the protocol allows it.
		if req.Op == "put" {
			drainUntilEOF(stream)
		}
		_ = protocol.WriteExit(stream, protocol.Exit{Code: -1, Err: err.Error()})
		return err
	}

	switch req.Op {
	case "put":
		if !allowWrite {
			drainUntilEOF(stream)
			err := fmt.Errorf("filetransfer: uploads not allowed (read-only)")
			_ = protocol.WriteExit(stream, protocol.Exit{Code: -1, Err: err.Error()})
			return err
		}
		return servePut(stream, abs, req)
	case "get":
		return serveGet(stream, abs, req)
	default:
		err := fmt.Errorf("filetransfer: unknown op %q", req.Op)
		_ = protocol.WriteExit(stream, protocol.Exit{Code: -1, Err: err.Error()})
		return err
	}
}

// servePut receives FrameData chunks into abs, resuming at req.Offset, then
// verifies the destination against req.Sha256 and acks the result.
func servePut(stream io.ReadWriter, abs string, req Request) error {
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		_ = protocol.WriteExit(stream, protocol.Exit{Code: -1, Err: err.Error()})
		return fmt.Errorf("filetransfer: mkdir: %w", err)
	}
	f, err := os.OpenFile(abs, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		_ = protocol.WriteExit(stream, protocol.Exit{Code: -1, Err: err.Error()})
		return fmt.Errorf("filetransfer: open dest: %w", err)
	}
	defer f.Close()
	if _, err := f.Seek(req.Offset, io.SeekStart); err != nil {
		_ = protocol.WriteExit(stream, protocol.Exit{Code: -1, Err: err.Error()})
		return fmt.Errorf("filetransfer: seek dest: %w", err)
	}

	if _, err := recvData(stream, f); err != nil {
		_ = protocol.WriteExit(stream, protocol.Exit{Code: -1, Err: err.Error()})
		return err
	}
	if err := f.Sync(); err != nil {
		_ = protocol.WriteExit(stream, protocol.Exit{Code: -1, Err: err.Error()})
		return fmt.Errorf("filetransfer: sync dest: %w", err)
	}

	return verifyAndAck(stream, abs, req.Sha256)
}

// serveGet streams abs from req.Offset to the client, then acks with the full
// file's sha256 so the client can verify the assembled result.
func serveGet(stream io.ReadWriter, abs string, req Request) error {
	f, err := os.Open(abs)
	if err != nil {
		_ = protocol.WriteExit(stream, protocol.Exit{Code: -1, Err: err.Error()})
		return fmt.Errorf("filetransfer: open source: %w", err)
	}
	defer f.Close()

	if req.Offset > 0 {
		if _, err := f.Seek(req.Offset, io.SeekStart); err != nil {
			_ = protocol.WriteExit(stream, protocol.Exit{Code: -1, Err: err.Error()})
			return fmt.Errorf("filetransfer: seek source: %w", err)
		}
	}

	if err := streamData(stream, f); err != nil {
		return err
	}
	if err := protocol.WriteFrame(stream, protocol.FrameEOF, nil); err != nil {
		return fmt.Errorf("filetransfer: send eof: %w", err)
	}

	sum, err := fileSha256(abs)
	if err != nil {
		_ = protocol.WriteExit(stream, protocol.Exit{Code: -1, Err: err.Error()})
		return err
	}
	return protocol.WriteExit(stream, protocol.Exit{Code: 0, Err: sum})
}

// streamData copies r to the stream as FrameData chunks. It does not send a
// terminating FrameEOF; callers do that.
func streamData(stream io.Writer, r io.Reader) error {
	buf := make([]byte, chunkSize)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if werr := protocol.WriteFrame(stream, protocol.FrameData, buf[:n]); werr != nil {
				return fmt.Errorf("filetransfer: write chunk: %w", werr)
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("filetransfer: read source: %w", err)
		}
	}
}

// recvData reads FrameData chunks into w until a FrameEOF arrives, returning the
// number of bytes written. A FrameExit before EOF is treated as an early abort
// carrying an error.
func recvData(stream io.Reader, w io.Writer) (int64, error) {
	var total int64
	for {
		ft, payload, err := protocol.ReadFrame(stream)
		if err != nil {
			return total, fmt.Errorf("filetransfer: read frame: %w", err)
		}
		switch ft {
		case protocol.FrameData:
			if _, err := w.Write(payload); err != nil {
				return total, fmt.Errorf("filetransfer: write dest: %w", err)
			}
			total += int64(len(payload))
		case protocol.FrameEOF:
			return total, nil
		case protocol.FrameExit:
			return total, exitError(payload)
		default:
			return total, fmt.Errorf("filetransfer: unexpected frame type %d", ft)
		}
	}
}

// drainUntilEOF discards inbound FrameData until FrameEOF so a rejected put does
// not leave unread bytes wedged in the stream.
func drainUntilEOF(stream io.Reader) {
	for {
		ft, _, err := protocol.ReadFrame(stream)
		if err != nil || ft == protocol.FrameEOF {
			return
		}
	}
}

// readAck reads the terminating FrameExit and turns a non-zero code into an
// error.
func readAck(stream io.Reader) error {
	ft, payload, err := protocol.ReadFrame(stream)
	if err != nil {
		return fmt.Errorf("filetransfer: read ack: %w", err)
	}
	if ft != protocol.FrameExit {
		return fmt.Errorf("filetransfer: expected exit ack, got type %d", ft)
	}
	return exitError(payload)
}

// verifyAndAck hashes abs, compares it to want, and sends the matching ack.
func verifyAndAck(stream io.Writer, abs, want string) error {
	got, err := fileSha256(abs)
	if err != nil {
		_ = protocol.WriteExit(stream, protocol.Exit{Code: -1, Err: err.Error()})
		return err
	}
	if want != "" && got != want {
		e := protocol.Exit{Code: 1, Err: fmt.Sprintf("sha256 mismatch: want %s got %s", want, got)}
		_ = protocol.WriteExit(stream, e)
		return errors.New(e.Err)
	}
	return protocol.WriteExit(stream, protocol.Exit{Code: 0, Err: got})
}

// exitError decodes a FrameExit payload and returns an error for non-zero codes.
// For a successful get the Err field carries the expected sha; that is not an
// error and is ignored here.
func exitError(payload []byte) error {
	var e protocol.Exit
	if err := json.Unmarshal(payload, &e); err != nil {
		return fmt.Errorf("filetransfer: decode exit: %w", err)
	}
	if e.Code != 0 {
		if e.Err != "" {
			return fmt.Errorf("filetransfer: remote error: %s", e.Err)
		}
		return fmt.Errorf("filetransfer: remote error code %d", e.Code)
	}
	return nil
}

// fileSha256 returns the hex-encoded sha256 of the file at path.
func fileSha256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("filetransfer: open for hash: %w", err)
	}
	defer f.Close()
	var h hash.Hash = sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("filetransfer: hash: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// resolve joins rel onto root and rejects any path that escapes root, blocking
// path-traversal attempts such as "../etc/passwd" or absolute paths.
func resolve(root, rel string) (string, error) {
	if rel == "" {
		return "", errors.New("filetransfer: empty path")
	}
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("filetransfer: resolve root: %w", err)
	}
	// Treat the requested path as relative to root even if it is absolute.
	joined := filepath.Join(cleanRoot, filepath.Clean("/"+rel))
	rel2, err := filepath.Rel(cleanRoot, joined)
	if err != nil {
		return "", fmt.Errorf("filetransfer: resolve path: %w", err)
	}
	if rel2 == ".." || strings.HasPrefix(rel2, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("filetransfer: path %q escapes root", rel)
	}
	return joined, nil
}
