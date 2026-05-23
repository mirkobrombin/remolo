// Package dirsync implements an incremental, rsync-like directory sync that
// runs over a single remolo channel (any io.ReadWriter).
//
// The design separates three concerns:
//
//   - manifest.go walks a directory tree and produces a Manifest: a map from
//     a forward-slash relative path to per-entry metadata (size, mtime, mode,
//     symlink target, and a sha256 of regular-file contents).
//   - delta.go implements a fixed-block rolling-checksum delta so a one-byte
//     edit in a large file moves roughly one block, not the whole file.
//   - sync.go frames a small protocol on top of internal/protocol and drives
//     the Push, Pull and Serve flows.
//
// The whole package is stdlib-only and cgo-free.
package dirsync

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// EntryKind classifies a manifest entry.
type EntryKind uint8

const (
	// KindFile is a regular file.
	KindFile EntryKind = iota
	// KindDir is a directory.
	KindDir
	// KindSymlink is a symbolic link; Target holds the (verbatim) link target.
	KindSymlink
)

// Entry describes one path in a tree. Paths are stored elsewhere (as the
// Manifest map key); an Entry carries only metadata.
type Entry struct {
	Kind   EntryKind   `json:"kind"`
	Size   int64       `json:"size,omitempty"`
	Mtime  int64       `json:"mtime,omitempty"` // unix seconds
	Mode   os.FileMode `json:"mode,omitempty"`  // permission bits (file type stripped)
	Sha256 string      `json:"sha256,omitempty"`
	Target string      `json:"target,omitempty"` // symlink target
}

// Manifest maps a forward-slash relative path to its Entry. The empty string
// key is never present; the root directory itself is implicit.
type Manifest map[string]Entry

// SortedPaths returns the manifest's keys sorted so that any parent directory
// sorts before its children. A plain lexical sort already guarantees this for
// slash-separated paths, so we just sort lexically.
func (m Manifest) SortedPaths() []string {
	out := make([]string, 0, len(m))
	for p := range m {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// BuildManifest walks root and returns a Manifest of everything not matched by
// the exclude predicate. Symlinks are recorded by their target and never
// followed. The root directory itself is not included as an entry.
//
// hashFiles controls whether regular files get a sha256 computed; the sender
// always needs hashes, a receiver that only needs the listing can pass false.
func BuildManifest(root string, ex *Excluder, hashFiles bool) (Manifest, error) {
	m := make(Manifest)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		isDir := d.IsDir()
		if ex != nil && ex.Match(rel, isDir) {
			if isDir {
				return filepath.SkipDir
			}
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, lerr := os.Readlink(p)
			if lerr != nil {
				return lerr
			}
			m[rel] = Entry{
				Kind:   KindSymlink,
				Mtime:  info.ModTime().Unix(),
				Mode:   info.Mode().Perm(),
				Target: target,
			}
		case isDir:
			m[rel] = Entry{
				Kind:  KindDir,
				Mtime: info.ModTime().Unix(),
				Mode:  info.Mode().Perm(),
			}
		case info.Mode().IsRegular():
			e := Entry{
				Kind:  KindFile,
				Size:  info.Size(),
				Mtime: info.ModTime().Unix(),
				Mode:  info.Mode().Perm(),
			}
			if hashFiles {
				sum, herr := hashFile(p)
				if herr != nil {
					return herr
				}
				e.Sha256 = sum
			}
			m[rel] = e
		default:
			// Skip sockets, devices, fifos: not transferable as content.
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return m, nil
}

// hashFile returns the hex sha256 of a file's contents.
func hashFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Excluder applies a small subset of .gitignore semantics: it supports the `*`
// wildcard (matching anything but a path separator) and directory-prefix
// patterns. A pattern is tested against both the full relative path and each
// path component, so a bare "node_modules" excludes the directory anywhere in
// the tree, while "build/*" or "src/gen" anchor against the relative path.
type Excluder struct {
	patterns []string
}

// NewExcluder compiles a set of glob-ish exclude patterns. Empty strings are
// ignored. The returned value is safe to share and never nil for a non-nil
// caller, but callers may also pass a nil *Excluder to BuildManifest.
func NewExcluder(patterns []string) *Excluder {
	cleaned := make([]string, 0, len(patterns))
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		p = strings.TrimSuffix(p, "/")
		if p == "" {
			continue
		}
		cleaned = append(cleaned, filepath.ToSlash(p))
	}
	return &Excluder{patterns: cleaned}
}

// Match reports whether rel (a forward-slash relative path) should be excluded.
func (e *Excluder) Match(rel string, isDir bool) bool {
	if e == nil || len(e.patterns) == 0 {
		return false
	}
	base := path.Base(rel)
	for _, pat := range e.patterns {
		// Whole-path match (handles anchored patterns like "build/out").
		if globMatch(pat, rel) {
			return true
		}
		// Basename match (handles bare names like "node_modules").
		if !strings.Contains(pat, "/") && globMatch(pat, base) {
			return true
		}
		// Directory-prefix match: a pattern "dir" excludes "dir/anything".
		if strings.HasPrefix(rel, pat+"/") {
			return true
		}
	}
	return false
}

// globMatch matches a single `*`-glob pattern (where `*` matches any run of
// non-separator characters) against name. It is a thin wrapper around the
// stdlib path.Match, whose semantics already match this requirement.
func globMatch(pattern, name string) bool {
	ok, err := path.Match(pattern, name)
	if err != nil {
		return false
	}
	return ok
}
