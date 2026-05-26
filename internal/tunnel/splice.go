// Package tunnel implements SSH-style port forwarding over the remolo mux:
// local forwarding (-L), remote forwarding (-R), and a dynamic SOCKS5 proxy
// (-D).
//
// The package owns none of the channel plumbing. Because it must not depend on
// remolo's session or protocol packages, every place that needs to create a
// transport channel is injected by the integrator through a callback. A
// "forward channel" is a single bidirectional stream whose far end the host has
// already wired to a dialled TCP target; the client opens one of these via an
// OpenForward callback, and the host services one by dialling the target and
// splicing.
package tunnel

import (
	"context"
	"io"
	"sync"
)

// OpenForward opens a remote forward channel to target and returns a stream
// already wired (the host side dials target). It is provided by the integrator
// and is the only way this package learns how to create channels. The returned
// value is closed by the caller when the forwarded connection ends.
type OpenForward func(ctx context.Context, target string) (io.ReadWriteCloser, error)

// Splice copies bytes bidirectionally between a and b until either direction
// ends, then closes both. It mirrors the relay package's splice but operates on
// io.ReadWriteCloser so it can join a local TCP conn to a mux stream.
func Splice(a io.ReadWriteCloser, b io.ReadWriteCloser) {
	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst io.ReadWriteCloser, src io.ReadWriteCloser) {
		defer wg.Done()
		io.Copy(dst, src)
		// Unblock the opposite direction by closing both ends.
		a.Close()
		b.Close()
	}
	go cp(a, b)
	go cp(b, a)
	wg.Wait()
}
