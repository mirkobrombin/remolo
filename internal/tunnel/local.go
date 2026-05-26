package tunnel

import (
	"context"
	"net"
)

// LocalForward implements SSH-style -L forwarding. It listens on listen (for
// example "127.0.0.1:8080") and, for every accepted local TCP connection, asks
// open for a remote forward channel wired to target and splices the two
// together. It returns when ctx is cancelled or the listener fails to accept
// (for example because it was closed). It logs nothing and surfaces errors via
// its return value.
func LocalForward(ctx context.Context, listen string, target string, open OpenForward) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", listen)
	if err != nil {
		return err
	}
	defer ln.Close()

	// Close the listener when ctx ends so the Accept loop unblocks and returns.
	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		local, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		go handleLocal(ctx, local, target, open)
	}
}

// handleLocal opens a forward channel for one accepted local connection and
// splices the two. On failure to open the channel it simply closes the local
// connection.
func handleLocal(ctx context.Context, local net.Conn, target string, open OpenForward) {
	remote, err := open(ctx, target)
	if err != nil {
		local.Close()
		return
	}
	Splice(local, remote)
}
