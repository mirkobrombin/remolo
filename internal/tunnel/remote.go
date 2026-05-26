package tunnel

import (
	"context"
	"io"
	"net"
)

// ServeRemoteForward implements the host side of SSH-style -R forwarding. The
// integrator supplies accept, which yields the next inbound forward stream
// (each corresponds to a connection accepted on the client's remote listener).
// For every accepted stream, ServeRemoteForward dials dialTarget afresh and
// splices the two. It returns when ctx is cancelled or accept reports an error.
func ServeRemoteForward(ctx context.Context, accept func(context.Context) (io.ReadWriteCloser, error), dialTarget string) error {
	for {
		stream, err := accept(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		go func(s io.ReadWriteCloser) {
			if derr := DialAndSplice(s, dialTarget); derr != nil {
				s.Close()
			}
		}(stream)
	}
}

// DialAndSplice is the host-side handler for a single forward channel: it dials
// target over TCP and splices the resulting connection to stream. The
// integrator can call it directly from its KindForward handler, passing the
// inbound stream and the Open.Target. On dial failure it returns the error
// without closing stream (the caller decides what to do); on success it blocks
// until either side closes.
func DialAndSplice(stream io.ReadWriteCloser, target string) error {
	conn, err := net.Dial("tcp", target)
	if err != nil {
		return err
	}
	Splice(stream, conn)
	return nil
}
