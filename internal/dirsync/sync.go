package dirsync

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mirkobrombin/remolo/internal/protocol"
)

// Options tune a sync run.
type Options struct {
	// Delete removes receiver files/dirs that are absent from the sender.
	Delete bool
	// DryRun computes and returns Stats without writing anything.
	DryRun bool
	// Excludes are glob/gitignore-lite patterns applied on the sender side.
	Excludes []string
}

// Stats summarises a completed (or dry-run) sync.
type Stats struct {
	FilesConsidered  int
	FilesTransferred int
	BytesTransferred int64
}

// Direction is the first thing a client tells the server: whether the client is
// pushing a tree to the server or pulling one from it.
type Direction string

const (
	dirPush Direction = "push"
	dirPull Direction = "pull"
)

// On-stream protocol.
//
// All control messages are FrameJSON frames carrying a msg envelope. Bulk byte
// streams (a receiver Signature, a sender delta, raw file bytes) travel as a
// run of FrameData frames terminated by a single FrameEOF frame.
//
// Handshake (always sent by the client, read by Serve):
//
//	-> msg{Type:"hello", Dir:push|pull, Root:remoteDir, Delete, DryRun}
//
// The party that owns the destination tree is the "receiver"; the party that
// owns the source tree is the "sender". For a push the client is the sender; for
// a pull the client is the receiver. The receiver always drives:
//
//	receiver -> msg{Type:"manifest"} + JSON-streamed sender manifest request
//
// Concretely the flow, once roles are fixed, is symmetric and goes:
//
//  1. sender computes its manifest (with hashes) and sends it:
//       sender   -> msg{Type:"manifest"} ; data stream = JSON(Manifest)
//  2. receiver diffs against its own tree and, per path needing transfer,
//     requests it. For regular files it first sends a block Signature of any
//     existing local copy:
//       receiver -> msg{Type:"need", Path, HasBase} ; if HasBase, data = JSON(Signature)
//     and a final
//       receiver -> msg{Type:"done"}
//  3. sender answers each "need" in order with the file's delta:
//       sender   -> msg{Type:"delta", Path, Size, Mode, Mtime} ; data = delta ops
//     then
//       sender   -> msg{Type:"complete", Stats...}
//  4. receiver applies deltas, recreates dirs/symlinks, optionally deletes
//     extras, and returns Stats.
//
// Keeping the receiver as the driver means both Push and Pull reduce to "run
// the sender loop" or "run the receiver loop" over the same stream.

type msg struct {
	Type    string      `json:"t"`
	Dir     Direction   `json:"dir,omitempty"`
	Root    string      `json:"root,omitempty"`
	Delete  bool        `json:"del,omitempty"`
	DryRun  bool        `json:"dry,omitempty"`
	Path    string      `json:"p,omitempty"`
	HasBase bool        `json:"hb,omitempty"`
	Size    int64       `json:"sz,omitempty"`
	Mode    os.FileMode `json:"mode,omitempty"`
	Mtime   int64       `json:"mt,omitempty"`
	Stats   *Stats      `json:"stats,omitempty"`
	Err     string      `json:"err,omitempty"`
}

// --- framed message helpers -------------------------------------------------

func writeMsg(w io.Writer, m msg) error {
	return protocol.WriteJSON(w, m)
}

func readMsg(r io.Reader) (msg, error) {
	t, payload, err := protocol.ReadFrame(r)
	if err != nil {
		return msg{}, err
	}
	if t != protocol.FrameJSON {
		return msg{}, fmt.Errorf("dirsync: expected JSON frame, got type %d", t)
	}
	var m msg
	if err := json.Unmarshal(payload, &m); err != nil {
		return msg{}, fmt.Errorf("dirsync: decode message: %w", err)
	}
	return m, nil
}

// writeStream sends b as one or more FrameData frames then a FrameEOF.
func writeStream(w io.Writer, b []byte) error {
	const chunk = 1 << 20
	for len(b) > 0 {
		n := len(b)
		if n > chunk {
			n = chunk
		}
		if err := protocol.WriteFrame(w, protocol.FrameData, b[:n]); err != nil {
			return err
		}
		b = b[n:]
	}
	return protocol.WriteFrame(w, protocol.FrameEOF, nil)
}

// streamReader presents the FrameData run up to FrameEOF as an io.Reader.
type streamReader struct {
	r   io.Reader
	buf []byte
	eof bool
}

func newStreamReader(r io.Reader) *streamReader { return &streamReader{r: r} }

func (s *streamReader) Read(p []byte) (int, error) {
	for len(s.buf) == 0 {
		if s.eof {
			return 0, io.EOF
		}
		t, payload, err := protocol.ReadFrame(s.r)
		if err != nil {
			return 0, err
		}
		switch t {
		case protocol.FrameData:
			s.buf = payload
		case protocol.FrameEOF:
			s.eof = true
			return 0, io.EOF
		default:
			return 0, fmt.Errorf("dirsync: unexpected frame type %d in stream", t)
		}
	}
	n := copy(p, s.buf)
	s.buf = s.buf[n:]
	return n, nil
}

// readStreamFull drains a FrameData/FrameEOF run into a byte slice.
func readStreamFull(r io.Reader) ([]byte, error) {
	return io.ReadAll(newStreamReader(r))
}

// --- public entry points ----------------------------------------------------

// Push sends localDir to the host (which is serving remoteDir). The client is
// the sender; the host is the receiver.
func Push(stream io.ReadWriter, localDir, remoteDir string, opt Options) (Stats, error) {
	if err := writeMsg(stream, msg{Type: "hello", Dir: dirPush, Root: remoteDir, Delete: opt.Delete, DryRun: opt.DryRun}); err != nil {
		return Stats{}, err
	}
	return runSender(stream, localDir, opt)
}

// Pull receives remoteDir from the host into localDir. The client is the
// receiver; the host is the sender.
func Pull(stream io.ReadWriter, remoteDir, localDir string, opt Options) (Stats, error) {
	if err := writeMsg(stream, msg{Type: "hello", Dir: dirPull, Root: remoteDir, Delete: opt.Delete, DryRun: opt.DryRun}); err != nil {
		return Stats{}, err
	}
	return runReceiver(stream, localDir, opt)
}

// Serve handles one sync session on the host side. It reads the client's hello,
// confirms the requested root stays inside root, and then runs whichever side
// (sender for a pull, receiver for a push) the client did not take.
func Serve(stream io.ReadWriter, root string) error {
	return ServeWithPolicy(stream, root, true)
}

// ServeWithPolicy is Serve with an explicit write policy: when allowWrite is
// false, push (client uploading to the host) is refused (read-only).
func ServeWithPolicy(stream io.ReadWriter, root string, allowWrite bool) error {
	hello, err := readMsg(stream)
	if err != nil {
		return err
	}
	if hello.Type != "hello" {
		return fmt.Errorf("dirsync: expected hello, got %q", hello.Type)
	}
	if !allowWrite && hello.Dir == dirPush {
		err := fmt.Errorf("dirsync: push not allowed (read-only)")
		_ = writeMsg(stream, msg{Type: "error", Err: err.Error()})
		return err
	}
	abs, err := resolveWithin(root, hello.Root)
	if err != nil {
		_ = writeMsg(stream, msg{Type: "error", Err: err.Error()})
		return err
	}
	opt := Options{Delete: hello.Delete, DryRun: hello.DryRun}
	switch hello.Dir {
	case dirPush:
		// Client is sender; host receives into abs.
		if !opt.DryRun {
			if err := os.MkdirAll(abs, 0o755); err != nil {
				return err
			}
		}
		_, err := runReceiver(stream, abs, opt)
		return err
	case dirPull:
		// Client is receiver; host sends abs. Excludes are host-side here, but
		// for a pull the host has no exclude list of its own; honour none.
		_, err := runSender(stream, abs, opt)
		return err
	default:
		return fmt.Errorf("dirsync: unknown direction %q", hello.Dir)
	}
}

// --- sender side ------------------------------------------------------------

// runSender owns the source tree. It ships its manifest, then answers the
// receiver's per-path "need" requests with deltas, and finally reports Stats.
func runSender(stream io.ReadWriter, dir string, opt Options) (Stats, error) {
	ex := NewExcluder(opt.Excludes)
	man, err := BuildManifest(dir, ex, true)
	if err != nil {
		return Stats{}, err
	}

	// Send the manifest.
	if err := writeMsg(stream, msg{Type: "manifest"}); err != nil {
		return Stats{}, err
	}
	mb, err := json.Marshal(man)
	if err != nil {
		return Stats{}, err
	}
	if err := writeStream(stream, mb); err != nil {
		return Stats{}, err
	}

	var stats Stats
	stats.FilesConsidered = countRegular(man)

	for {
		m, err := readMsg(stream)
		if err != nil {
			return Stats{}, err
		}
		switch m.Type {
		case "need":
			entry, ok := man[m.Path]
			if !ok || entry.Kind != KindFile {
				return Stats{}, fmt.Errorf("dirsync: receiver requested unknown file %q", m.Path)
			}
			var sig Signature
			if m.HasBase {
				sigBytes, err := readStreamFull(stream)
				if err != nil {
					return Stats{}, err
				}
				if err := json.Unmarshal(sigBytes, &sig); err != nil {
					return Stats{}, err
				}
			} else {
				sig = Signature{BlockSize: BlockSize}
			}
			full := filepath.Join(dir, filepath.FromSlash(m.Path))
			if err := writeMsg(stream, msg{
				Type:  "delta",
				Path:  m.Path,
				Size:  entry.Size,
				Mode:  entry.Mode,
				Mtime: entry.Mtime,
			}); err != nil {
				return Stats{}, err
			}
			f, err := os.Open(full)
			if err != nil {
				return Stats{}, err
			}
			var buf bytes.Buffer
			literal, derr := WriteDelta(&buf, f, sig)
			f.Close()
			if derr != nil {
				return Stats{}, derr
			}
			if err := writeStream(stream, buf.Bytes()); err != nil {
				return Stats{}, err
			}
			stats.FilesTransferred++
			stats.BytesTransferred += literal
		case "done":
			// Receiver is finished requesting; it will send the authoritative
			// Stats it computed (it knows about deletions and dry-run).
			final, err := readMsg(stream)
			if err != nil {
				return Stats{}, err
			}
			if final.Type == "error" {
				return Stats{}, errors.New(final.Err)
			}
			if final.Stats != nil {
				return *final.Stats, nil
			}
			return stats, nil
		case "error":
			return Stats{}, errors.New(m.Err)
		default:
			return Stats{}, fmt.Errorf("dirsync: sender got unexpected message %q", m.Type)
		}
	}
}

// --- receiver side ----------------------------------------------------------

// runReceiver owns the destination tree. It reads the sender's manifest, diffs
// it against the local tree, requests each file that differs (sending a base
// signature when it has a local copy), applies the returned deltas, recreates
// dirs and symlinks, optionally deletes extras, and reports authoritative Stats.
func runReceiver(stream io.ReadWriter, dir string, opt Options) (Stats, error) {
	// Receive the sender's manifest.
	m, err := readMsg(stream)
	if err != nil {
		return Stats{}, err
	}
	if m.Type != "manifest" {
		if m.Type == "error" {
			return Stats{}, errors.New(m.Err)
		}
		return Stats{}, fmt.Errorf("dirsync: expected manifest, got %q", m.Type)
	}
	mb, err := readStreamFull(stream)
	if err != nil {
		return Stats{}, err
	}
	var sender Manifest
	if err := json.Unmarshal(mb, &sender); err != nil {
		return Stats{}, err
	}

	var stats Stats
	stats.FilesConsidered = countRegular(sender)

	// Create directories first, in parent-before-child order.
	paths := sender.SortedPaths()
	for _, p := range paths {
		e := sender[p]
		if e.Kind != KindDir {
			continue
		}
		if opt.DryRun {
			continue
		}
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(full, e.Mode.Perm()|0o100); err != nil {
			return Stats{}, err
		}
		_ = os.Chmod(full, e.Mode.Perm())
	}

	// Symlinks.
	for _, p := range paths {
		e := sender[p]
		if e.Kind != KindSymlink {
			continue
		}
		if opt.DryRun {
			continue
		}
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := recreateSymlink(full, e.Target); err != nil {
			return Stats{}, err
		}
	}

	// Regular files: request those that are missing or whose sha256 differs.
	for _, p := range paths {
		e := sender[p]
		if e.Kind != KindFile {
			continue
		}
		full := filepath.Join(dir, filepath.FromSlash(p))
		localSum, localExists := localHash(full)
		if localExists && localSum == e.Sha256 {
			continue // identical, skip entirely (no-op rerun)
		}
		if opt.DryRun {
			stats.FilesTransferred++
			// Best-effort byte estimate for dry-run: if no local base, the whole
			// file; otherwise unknown, count size as upper bound.
			stats.BytesTransferred += e.Size
			continue
		}
		if err := requestAndApply(stream, dir, p, e, full, localExists); err != nil {
			return Stats{}, err
		}
		stats.FilesTransferred++
	}

	// Optional deletion of receiver entries absent from the sender manifest.
	if opt.Delete && !opt.DryRun {
		if err := deleteExtras(dir, sender); err != nil {
			return Stats{}, err
		}
	}

	// The receiver's Stats are the authoritative ones: only it knows about
	// deletions and the dry-run estimates. Tell the sender we are finished, then
	// send them.
	if err := writeMsg(stream, msg{Type: "done"}); err != nil {
		return Stats{}, err
	}
	if err := writeMsg(stream, msg{Type: "complete", Stats: &stats}); err != nil {
		return Stats{}, err
	}
	return stats, nil
}

// requestAndApply requests path p from the sender (supplying a block signature
// of any existing local copy), then applies the returned delta to a temp file
// and atomically renames it into place, preserving mode and mtime.
func requestAndApply(stream io.ReadWriter, dir, p string, e Entry, full string, hasBase bool) error {
	if err := writeMsg(stream, msg{Type: "need", Path: p, HasBase: hasBase}); err != nil {
		return err
	}
	if hasBase {
		base, err := os.Open(full)
		if err != nil {
			return err
		}
		sig, serr := BuildSignature(base)
		base.Close()
		if serr != nil {
			return serr
		}
		sb, err := json.Marshal(sig)
		if err != nil {
			return err
		}
		if err := writeStream(stream, sb); err != nil {
			return err
		}
	}

	dm, err := readMsg(stream)
	if err != nil {
		return err
	}
	if dm.Type != "delta" {
		if dm.Type == "error" {
			return errors.New(dm.Err)
		}
		return fmt.Errorf("dirsync: expected delta, got %q", dm.Type)
	}

	// Open the base (read-only) to satisfy opCopy instructions. If there is no
	// base, use an empty reader; opCopy will then never appear.
	var base io.ReaderAt = bytes.NewReader(nil)
	var baseSize int64
	var baseFile *os.File
	if hasBase {
		bf, err := os.Open(full)
		if err != nil {
			return err
		}
		baseFile = bf
		if st, err := bf.Stat(); err == nil {
			baseSize = st.Size()
		}
		base = bf
	}

	tmp, err := os.CreateTemp(filepath.Dir(full), ".remolo-sync-*")
	if err != nil {
		if baseFile != nil {
			baseFile.Close()
		}
		return err
	}
	tmpName := tmp.Name()
	sr := newStreamReader(stream)
	applyErr := ApplyDelta(tmp, base, baseSize, BlockSize, sr)
	// ApplyDelta stops at opEnd, which arrives before the stream's terminating
	// FrameEOF. Drain the rest of the FrameData/FrameEOF run so the stream is
	// positioned at the next control message for both peers.
	if applyErr == nil {
		_, applyErr = io.Copy(io.Discard, sr)
	}
	closeErr := tmp.Close()
	if baseFile != nil {
		baseFile.Close()
	}
	if applyErr != nil {
		os.Remove(tmpName)
		return applyErr
	}
	if closeErr != nil {
		os.Remove(tmpName)
		return closeErr
	}

	if err := os.Chmod(tmpName, dm.Mode.Perm()); err != nil {
		os.Remove(tmpName)
		return err
	}
	mt := time.Unix(dm.Mtime, 0)
	_ = os.Chtimes(tmpName, mt, mt)

	if err := os.Rename(tmpName, full); err != nil {
		os.Remove(tmpName)
		return err
	}
	// Rename can drop mtime metadata on some filesystems; reassert it.
	_ = os.Chtimes(full, mt, mt)
	return nil
}

// --- helpers ----------------------------------------------------------------

// resolveWithin joins root and rel and verifies the result stays inside root,
// rejecting traversal (".." escapes, absolute escapes). An empty rel resolves
// to root itself.
func resolveWithin(root, rel string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	rel = filepath.FromSlash(rel)
	if filepath.IsAbs(rel) {
		// Allow an absolute path only if it is already inside root.
		clean := filepath.Clean(rel)
		if clean == absRoot || strings.HasPrefix(clean, absRoot+string(os.PathSeparator)) {
			return clean, nil
		}
		return "", fmt.Errorf("dirsync: path %q escapes root", rel)
	}
	joined := filepath.Join(absRoot, rel)
	clean := filepath.Clean(joined)
	if clean != absRoot && !strings.HasPrefix(clean, absRoot+string(os.PathSeparator)) {
		return "", fmt.Errorf("dirsync: path %q escapes root", rel)
	}
	return clean, nil
}

func countRegular(m Manifest) int {
	n := 0
	for _, e := range m {
		if e.Kind == KindFile {
			n++
		}
	}
	return n
}

// localHash returns the sha256 of a local regular file and whether it exists as
// a regular file.
func localHash(full string) (string, bool) {
	info, err := os.Lstat(full)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	sum, err := hashFile(full)
	if err != nil {
		return "", false
	}
	return sum, true
}

// recreateSymlink ensures a symlink at full points to target, replacing any
// existing entry there.
func recreateSymlink(full, target string) error {
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	if existing, err := os.Readlink(full); err == nil {
		if existing == target {
			return nil
		}
		if err := os.Remove(full); err != nil {
			return err
		}
	} else if _, err := os.Lstat(full); err == nil {
		// A non-symlink occupies the path; replace it.
		if err := os.RemoveAll(full); err != nil {
			return err
		}
	}
	return os.Symlink(target, full)
}

// deleteExtras removes receiver-side files, symlinks and directories that do
// not appear in the sender manifest. Directories are removed after their
// contents (deepest first).
func deleteExtras(dir string, sender Manifest) error {
	var toRemove []string
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if p == dir {
			return nil
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if _, ok := sender[rel]; !ok {
			toRemove = append(toRemove, p)
			if info.IsDir() {
				return filepath.SkipDir
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	// Remove deepest paths first so directories empty out before removal.
	sort.Slice(toRemove, func(i, j int) bool {
		return len(toRemove[i]) > len(toRemove[j])
	})
	for _, p := range toRemove {
		if err := os.RemoveAll(p); err != nil {
			return err
		}
	}
	return nil
}
