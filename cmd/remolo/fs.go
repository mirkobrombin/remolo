package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/mirkobrombin/go-cli-builder/v2/pkg/cli"
	"github.com/mirkobrombin/remolo/internal/filetransfer"
	"github.com/mirkobrombin/remolo/internal/protocol"
	"github.com/mirkobrombin/remolo/internal/rpc"
	"github.com/mirkobrombin/remolo/internal/session"
	"github.com/mirkobrombin/remolo/internal/token"
)

// FsCmd is an interactive remote file browser (sftp-style REPL) over the RPC and
// file-transfer channels: ls, cd, pwd, stat, get, put.
type FsCmd struct {
	Token string `arg:"" required:"true" help:"The session token"`

	cli.Base
}

func (c *FsCmd) Run() error {
	tok, err := decodeToken(c.Token)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// One long-lived RPC channel drives ls/cd/stat. File transfers open their
	// own channels (reusing the mux daemon) on demand.
	cs, err := openChannel(ctx, tok, protocol.Open{Kind: protocol.KindRPC}, session.ConnectOptions{}, c.Logger, true)
	if err != nil {
		return err
	}
	defer cs.cleanup()
	client := rpc.NewClient(cs.stream)

	cwd := "."
	fmt.Println("remolo fs: interactive remote files. Commands: ls [path], cd <path>, pwd, stat <path>, get <remote> [local], put <local> <remote>, exit")
	in := bufio.NewScanner(os.Stdin)
	for {
		fmt.Printf("remote:%s$ ", cwd)
		if !in.Scan() {
			break
		}
		fields := strings.Fields(in.Text())
		if len(fields) == 0 {
			continue
		}
		cmd, args := fields[0], fields[1:]
		switch cmd {
		case "exit", "quit":
			return nil
		case "pwd":
			fmt.Println(cwd)
		case "ls":
			p := cwd
			if len(args) > 0 {
				p = resolveRemote(cwd, args[0])
			}
			var entries []rpc.DirEntry
			if err := client.Call("ls", rpc.PathParams{Path: p}, &entries); err != nil {
				fmt.Println("ls:", err)
				continue
			}
			for _, e := range entries {
				suffix := ""
				if e.IsDir {
					suffix = "/"
				}
				fmt.Printf("  %10d  %s%s\n", e.Size, e.Name, suffix)
			}
		case "cd":
			if len(args) == 0 {
				cwd = "."
				continue
			}
			p := resolveRemote(cwd, args[0])
			var st rpc.StatResult
			if err := client.Call("stat", rpc.PathParams{Path: p}, &st); err != nil {
				fmt.Println("cd:", err)
				continue
			}
			if !st.IsDir {
				fmt.Println("cd: not a directory:", p)
				continue
			}
			cwd = p
		case "stat":
			if len(args) == 0 {
				fmt.Println("usage: stat <path>")
				continue
			}
			var st rpc.StatResult
			if err := client.Call("stat", rpc.PathParams{Path: resolveRemote(cwd, args[0])}, &st); err != nil {
				fmt.Println("stat:", err)
				continue
			}
			fmt.Printf("  name=%s size=%d dir=%v mode=%s\n", st.Name, st.Size, st.IsDir, st.Mode)
		case "get":
			if len(args) == 0 {
				fmt.Println("usage: get <remote> [local]")
				continue
			}
			remote := resolveRemote(cwd, args[0])
			local := path.Base(args[0])
			if len(args) > 1 {
				local = args[1]
			}
			if err := fsTransfer(ctx, tok, c.Logger, false, local, remote); err != nil {
				fmt.Println("get:", err)
				continue
			}
			fmt.Printf("  downloaded %s -> %s\n", remote, local)
		case "put":
			if len(args) < 2 {
				fmt.Println("usage: put <local> <remote>")
				continue
			}
			remote := resolveRemote(cwd, args[1])
			if err := fsTransfer(ctx, tok, c.Logger, true, args[0], remote); err != nil {
				fmt.Println("put:", err)
				continue
			}
			fmt.Printf("  uploaded %s -> %s\n", args[0], remote)
		default:
			fmt.Println("unknown command:", cmd)
		}
	}
	return nil
}

// resolveRemote joins a possibly-relative path against the current remote dir.
func resolveRemote(cwd, p string) string {
	if strings.HasPrefix(p, "/") {
		return path.Clean(p)
	}
	return path.Clean(path.Join(cwd, p))
}

// fsTransfer opens a fresh file channel (reusing the mux daemon) and runs a put
// or get.
func fsTransfer(ctx context.Context, tok *token.Token, log session.Logger, put bool, local, remote string) error {
	cs, err := openChannel(ctx, tok, protocol.Open{Kind: protocol.KindFile}, session.ConnectOptions{}, log, false)
	if err != nil {
		return err
	}
	defer cs.cleanup()
	if put {
		return filetransfer.Put(cs.stream, local, remote)
	}
	_, err = filetransfer.Get(cs.stream, remote, local)
	return err
}
