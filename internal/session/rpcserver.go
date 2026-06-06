package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mirkobrombin/remolo/internal/rpc"
)

// FileInfoMsg is the wire shape for file metadata in the FUSE-backing RPC ops.
type FileInfoMsg struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Mode    uint32 `json:"mode"`
	IsDir   bool   `json:"is_dir"`
	ModTime int64  `json:"mod_time"`
}

// hostRPCServer builds the RPC server for a KindRPC channel: the default methods
// (sysinfo, exec, ls, stat), the ranged file operations that back a FUSE mount,
// and client enrollment, all rooted at the host file root and honouring the
// read-only policy.
func hostRPCServer(h *Host) *rpc.Server {
	root := h.fileRoot
	readOnly := h.scope.readOnly
	s := rpc.DefaultServer()

	// enroll: register a client public key so it can connect later without a
	// token (the caller is already token-authenticated on this channel).
	s.Register("enroll", func(_ context.Context, params json.RawMessage) (any, error) {
		if h.authorized == nil {
			return nil, fmt.Errorf("enrollment not available")
		}
		var p struct {
			PubKey []byte `json:"pubkey"`
			Label  string `json:"label"`
		}
		json.Unmarshal(params, &p)
		if len(p.PubKey) != 32 {
			return nil, fmt.Errorf("invalid client key")
		}
		if err := h.authorized.Add(p.PubKey, p.Label); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil
	})
	resolve := func(p string) (string, error) { return rootedPath(root, p) }
	writable := func() error {
		if readOnly {
			return fmt.Errorf("read-only")
		}
		return nil
	}

	s.Register("fstat", func(_ context.Context, params json.RawMessage) (any, error) {
		var p struct {
			Path string `json:"path"`
		}
		json.Unmarshal(params, &p)
		abs, err := resolve(p.Path)
		if err != nil {
			return nil, err
		}
		fi, err := os.Lstat(abs)
		if err != nil {
			return nil, err
		}
		return toMsg(fi), nil
	})

	s.Register("flist", func(_ context.Context, params json.RawMessage) (any, error) {
		var p struct {
			Path string `json:"path"`
		}
		json.Unmarshal(params, &p)
		abs, err := resolve(p.Path)
		if err != nil {
			return nil, err
		}
		ents, err := os.ReadDir(abs)
		if err != nil {
			return nil, err
		}
		out := make([]FileInfoMsg, 0, len(ents))
		for _, e := range ents {
			fi, ierr := e.Info()
			if ierr != nil {
				continue
			}
			out = append(out, toMsg(fi))
		}
		return out, nil
	})

	s.Register("pread", func(_ context.Context, params json.RawMessage) (any, error) {
		var p struct {
			Path string `json:"path"`
			Off  int64  `json:"off"`
			Len  int    `json:"len"`
		}
		json.Unmarshal(params, &p)
		abs, err := resolve(p.Path)
		if err != nil {
			return nil, err
		}
		f, err := os.Open(abs)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		buf := make([]byte, p.Len)
		n, rerr := f.ReadAt(buf, p.Off)
		if rerr != nil && n == 0 {
			return map[string]any{"data": []byte{}, "eof": true}, nil
		}
		return map[string]any{"data": buf[:n]}, nil
	})

	s.Register("pwrite", func(_ context.Context, params json.RawMessage) (any, error) {
		if err := writable(); err != nil {
			return nil, err
		}
		var p struct {
			Path string `json:"path"`
			Off  int64  `json:"off"`
			Data []byte `json:"data"`
		}
		json.Unmarshal(params, &p)
		abs, err := resolve(p.Path)
		if err != nil {
			return nil, err
		}
		f, err := os.OpenFile(abs, os.O_WRONLY|os.O_CREATE, 0o644)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		n, werr := f.WriteAt(p.Data, p.Off)
		if werr != nil {
			return nil, werr
		}
		return map[string]any{"n": n}, nil
	})

	mutate := func(name string, fn func(p struct {
		Path string `json:"path"`
		New  string `json:"new"`
		Size int64  `json:"size"`
		Mode uint32 `json:"mode"`
	}, abs string) error) {
		s.Register(name, func(_ context.Context, params json.RawMessage) (any, error) {
			if err := writable(); err != nil {
				return nil, err
			}
			var p struct {
				Path string `json:"path"`
				New  string `json:"new"`
				Size int64  `json:"size"`
				Mode uint32 `json:"mode"`
			}
			json.Unmarshal(params, &p)
			abs, err := resolve(p.Path)
			if err != nil {
				return nil, err
			}
			if err := fn(p, abs); err != nil {
				return nil, err
			}
			return map[string]any{"ok": true}, nil
		})
	}

	mutate("ftruncate", func(p struct {
		Path string `json:"path"`
		New  string `json:"new"`
		Size int64  `json:"size"`
		Mode uint32 `json:"mode"`
	}, abs string) error {
		return os.Truncate(abs, p.Size)
	})
	mutate("fcreate", func(p struct {
		Path string `json:"path"`
		New  string `json:"new"`
		Size int64  `json:"size"`
		Mode uint32 `json:"mode"`
	}, abs string) error {
		f, err := os.OpenFile(abs, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(p.Mode))
		if err != nil {
			return err
		}
		return f.Close()
	})
	mutate("fmkdir", func(p struct {
		Path string `json:"path"`
		New  string `json:"new"`
		Size int64  `json:"size"`
		Mode uint32 `json:"mode"`
	}, abs string) error {
		return os.Mkdir(abs, os.FileMode(p.Mode))
	})
	mutate("fremove", func(p struct {
		Path string `json:"path"`
		New  string `json:"new"`
		Size int64  `json:"size"`
		Mode uint32 `json:"mode"`
	}, abs string) error {
		return os.Remove(abs)
	})
	mutate("frename", func(p struct {
		Path string `json:"path"`
		New  string `json:"new"`
		Size int64  `json:"size"`
		Mode uint32 `json:"mode"`
	}, abs string) error {
		newAbs, err := rootedPath(root, p.New)
		if err != nil {
			return err
		}
		return os.Rename(abs, newAbs)
	})

	return s
}

func toMsg(fi os.FileInfo) FileInfoMsg {
	return FileInfoMsg{
		Name:    fi.Name(),
		Size:    fi.Size(),
		Mode:    uint32(fi.Mode().Perm()),
		IsDir:   fi.IsDir(),
		ModTime: fi.ModTime().Unix(),
	}
}

// rootedPath resolves p inside root, rejecting traversal outside it.
func rootedPath(root, p string) (string, error) {
	clean := filepath.Clean("/" + strings.TrimPrefix(p, "/"))
	abs := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes root: %q", p)
	}
	return abs, nil
}
