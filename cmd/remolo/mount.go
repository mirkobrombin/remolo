package main

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mirkobrombin/go-cli-builder/v2/pkg/cli"
	"github.com/mirkobrombin/remolo/internal/fusefs"
	"github.com/mirkobrombin/remolo/internal/protocol"
	"github.com/mirkobrombin/remolo/internal/rpc"
	"github.com/mirkobrombin/remolo/internal/session"
)

// MountCmd mounts the host filesystem locally over FUSE. All I/O rides the
// remolo connection, so the mount works behind NAT.
type MountCmd struct {
	Token      string `arg:"" required:"true" help:"The session token"`
	Mountpoint string `arg:"" required:"true" help:"Local directory to mount onto"`
	ReadOnly   bool   `cli:"read-only,r" help:"Mount read-only"`

	cli.Base
}

func (c *MountCmd) Run() error {
	tok, err := decodeToken(c.Token)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// One persistent connection for the mount's lifetime; each FUSE op opens its
	// own RPC channel on it (so the kernel's parallel requests do not serialise).
	cl, err := session.ConnectWith(ctx, tok, session.ConnectOptions{}, nil)
	if err != nil {
		return err
	}
	defer cl.Close("unmounted")

	backend := &rpcBackend{ctx: ctx, cl: cl}
	srv, err := fusefs.Mount(c.Mountpoint, backend, fusefs.Options{ReadOnly: c.ReadOnly})
	if err != nil {
		if fusefs.IsUnavailable(err) {
			return fmt.Errorf("FUSE not available here (need /dev/fuse and permission, Linux/macOS only): %w", err)
		}
		return err
	}
	fmt.Printf("remolo: mounted host filesystem at %s (Ctrl-C to unmount)\n", c.Mountpoint)

	go func() { <-ctx.Done(); srv.Unmount() }()
	return srv.Wait()
}

// rpcBackend implements fusefs.Backend over remolo RPC channels.
type rpcBackend struct {
	ctx context.Context
	cl  *session.Client
}

func (b *rpcBackend) call(method string, params, result any) error {
	stream, err := b.cl.OpenChannel(b.ctx, protocol.Open{Kind: protocol.KindRPC})
	if err != nil {
		return err
	}
	defer stream.Close()
	return mapError(rpc.Call(stream, method, params, result))
}

// mapError restores recognisable error kinds lost when the host's error crosses
// the RPC boundary as a plain string, so the FUSE layer can map them to the
// right errno (ENOENT on lookup is essential for create-on-write to work).
func mapError(err error) error {
	if err == nil {
		return nil
	}
	s := err.Error()
	switch {
	case strings.Contains(s, "no such file"), strings.Contains(s, "not exist"):
		return fs.ErrNotExist
	case strings.Contains(s, "file exists"):
		return fs.ErrExist
	case strings.Contains(s, "permission denied"), strings.Contains(s, "read-only"):
		return fs.ErrPermission
	default:
		return err
	}
}

func toFI(m session.FileInfoMsg) fusefs.FileInfo {
	return fusefs.FileInfo{Name: m.Name, Size: m.Size, Mode: m.Mode, IsDir: m.IsDir, ModTime: m.ModTime}
}

func (b *rpcBackend) List(path string) ([]fusefs.FileInfo, error) {
	var msgs []session.FileInfoMsg
	if err := b.call("flist", map[string]any{"path": path}, &msgs); err != nil {
		return nil, err
	}
	out := make([]fusefs.FileInfo, len(msgs))
	for i, m := range msgs {
		out[i] = toFI(m)
	}
	return out, nil
}

func (b *rpcBackend) Stat(path string) (fusefs.FileInfo, error) {
	var m session.FileInfoMsg
	if err := b.call("fstat", map[string]any{"path": path}, &m); err != nil {
		return fusefs.FileInfo{}, err
	}
	return toFI(m), nil
}

func (b *rpcBackend) ReadAt(path string, off int64, p []byte) (int, error) {
	var res struct {
		Data []byte `json:"data"`
		Eof  bool   `json:"eof"`
	}
	if err := b.call("pread", map[string]any{"path": path, "off": off, "len": len(p)}, &res); err != nil {
		return 0, err
	}
	if len(res.Data) == 0 && res.Eof {
		return 0, io.EOF
	}
	n := copy(p, res.Data)
	return n, nil
}

func (b *rpcBackend) WriteAt(path string, off int64, p []byte) (int, error) {
	var res struct {
		N int `json:"n"`
	}
	if err := b.call("pwrite", map[string]any{"path": path, "off": off, "data": p}, &res); err != nil {
		return 0, err
	}
	return res.N, nil
}

func (b *rpcBackend) Truncate(path string, size int64) error {
	return b.call("ftruncate", map[string]any{"path": path, "size": size}, &struct{}{})
}

func (b *rpcBackend) Create(path string, mode uint32) error {
	return b.call("fcreate", map[string]any{"path": path, "mode": mode}, &struct{}{})
}

func (b *rpcBackend) Mkdir(path string, mode uint32) error {
	return b.call("fmkdir", map[string]any{"path": path, "mode": mode}, &struct{}{})
}

func (b *rpcBackend) Remove(path string) error {
	return b.call("fremove", map[string]any{"path": path}, &struct{}{})
}

func (b *rpcBackend) Rename(oldp, newp string) error {
	return b.call("frename", map[string]any{"path": oldp, "new": newp}, &struct{}{})
}
