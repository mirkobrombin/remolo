package tunnel

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"testing"
	"time"
)

// startEchoServer stands up a real TCP server that echoes every byte it reads
// back to the sender. It returns the listen address and a stop function.
func startEchoServer(t *testing.T) (addr string, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				io.Copy(conn, conn)
			}(c)
		}
	}()
	return ln.Addr().String(), func() { ln.Close() }
}

// dialOpener returns an OpenForward that ignores the requested target and
// always dials the fixed echo target directly, simulating the remote side
// having wired the channel to that target.
func dialOpener(echoAddr string) OpenForward {
	return func(ctx context.Context, target string) (io.ReadWriteCloser, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", echoAddr)
	}
}

// echoOpener returns an OpenForward that honours the requested target, used by
// the SOCKS test so the address parsing path is exercised end to end.
func echoOpener() OpenForward {
	return func(ctx context.Context, target string) (io.ReadWriteCloser, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", target)
	}
}

func roundTrip(t *testing.T, c net.Conn, payload string) {
	t.Helper()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(c, payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != payload {
		t.Fatalf("echo mismatch: got %q want %q", string(buf), payload)
	}
}

func TestLocalForward(t *testing.T) {
	echoAddr, stopEcho := startEchoServer(t)
	defer stopEcho()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// LocalForward does not report the address it bound, so the test has to
	// pick one up front. Take a free port from a throwaway listener and hand it
	// over; the window between close and rebind is small enough in practice.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe listen: %v", err)
	}
	listenAddr := probe.Addr().String()
	probe.Close()

	errCh := make(chan error, 1)
	go func() {
		errCh <- LocalForward(ctx, listenAddr, "ignored:0", dialOpener(echoAddr))
	}()

	c := dialWithRetry(t, listenAddr)
	defer c.Close()
	roundTrip(t, c, "hello local forward")

	cancel()
	if err := <-errCh; err != context.Canceled {
		t.Fatalf("LocalForward returned %v, want context.Canceled", err)
	}
}

func TestSocksForward(t *testing.T) {
	echoAddr, stopEcho := startEchoServer(t)
	defer stopEcho()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe listen: %v", err)
	}
	socksAddr := probe.Addr().String()
	probe.Close()

	errCh := make(chan error, 1)
	go func() {
		errCh <- SocksForward(ctx, socksAddr, echoOpener())
	}()

	c := dialWithRetry(t, socksAddr)
	defer c.Close()

	socksConnect(t, c, echoAddr)
	roundTrip(t, c, "hello socks forward")

	cancel()
	if err := <-errCh; err != context.Canceled {
		t.Fatalf("SocksForward returned %v, want context.Canceled", err)
	}
}

func TestDialAndSplice(t *testing.T) {
	echoAddr, stopEcho := startEchoServer(t)
	defer stopEcho()

	// One side of the pipe stands in for the mux stream; the test drives the
	// other side as the application.
	streamSide, appSide := net.Pipe()

	done := make(chan error, 1)
	go func() {
		done <- DialAndSplice(streamSide, echoAddr)
	}()

	roundTrip(t, appSide, "hello dial and splice")

	appSide.Close()
	if err := <-done; err != nil {
		t.Fatalf("DialAndSplice: %v", err)
	}
}

// dialWithRetry connects to addr, retrying briefly while the forwarder's
// listener comes up.
func dialWithRetry(t *testing.T, addr string) net.Conn {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := net.Dial("tcp", addr)
		if err == nil {
			return c
		}
		if time.Now().After(deadline) {
			t.Fatalf("dial %s: %v", addr, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// socksConnect performs a minimal SOCKS5 no-auth CONNECT handshake to target
// over c, failing the test on any protocol error.
func socksConnect(t *testing.T, c net.Conn, target string) {
	t.Helper()
	c.SetDeadline(time.Now().Add(5 * time.Second))

	// Method negotiation: VER=5, NMETHODS=1, METHOD=no-auth.
	if _, err := c.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("socks negotiate write: %v", err)
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(c, resp); err != nil {
		t.Fatalf("socks negotiate read: %v", err)
	}
	if resp[0] != 0x05 || resp[1] != 0x00 {
		t.Fatalf("socks method reply %v", resp)
	}

	host, portStr, err := net.SplitHostPort(target)
	if err != nil {
		t.Fatalf("split target: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("port: %v", err)
	}

	// CONNECT request using DOMAINNAME so the domain path is exercised.
	req := []byte{0x05, 0x01, 0x00, socksAddrDomain, byte(len(host))}
	req = append(req, []byte(host)...)
	portBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(portBytes, uint16(port))
	req = append(req, portBytes...)
	if _, err := c.Write(req); err != nil {
		t.Fatalf("socks connect write: %v", err)
	}

	// Reply: VER, REP, RSV, ATYP, BND.ADDR, BND.PORT. We assume IPv4 ATYP as
	// written by the server.
	head := make([]byte, 4)
	if _, err := io.ReadFull(c, head); err != nil {
		t.Fatalf("socks reply head: %v", err)
	}
	if head[1] != socksReplySuccess {
		t.Fatalf("socks reply status %d", head[1])
	}
	var addrLen int
	switch head[3] {
	case socksAddrIPv4:
		addrLen = net.IPv4len
	case socksAddrIPv6:
		addrLen = net.IPv6len
	default:
		t.Fatalf("unexpected reply ATYP %d", head[3])
	}
	rest := make([]byte, addrLen+2)
	if _, err := io.ReadFull(c, rest); err != nil {
		t.Fatalf("socks reply addr: %v", err)
	}

	// Reset the deadline for the caller's echo round-trip.
	c.SetDeadline(time.Time{})
}
