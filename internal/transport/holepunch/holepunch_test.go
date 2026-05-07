package holepunch

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestPunchTwoLocalConns(t *testing.T) {
	// Bind two local UDP sockets and have one punch towards the other; the
	// receiver must observe the marker datagrams.
	recv, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen recv: %v", err)
	}
	defer recv.Close()

	send, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen send: %v", err)
	}
	defer send.Close()

	const rounds = 3
	done := make(chan int, 1)
	go func() {
		buf := make([]byte, 64)
		count := 0
		_ = recv.SetReadDeadline(time.Now().Add(3 * time.Second))
		for count < rounds {
			n, _, err := recv.ReadFromUDP(buf)
			if err != nil {
				break
			}
			if string(buf[:n]) == "remolo-punch" {
				count++
			}
		}
		done <- count
	}()

	if err := Punch(send, recv.LocalAddr().String(), rounds, 20*time.Millisecond); err != nil {
		t.Fatalf("Punch: %v", err)
	}

	select {
	case got := <-done:
		if got != rounds {
			t.Fatalf("received %d punch packets, want %d", got, rounds)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("timed out waiting for punch packets")
	}
}

func TestPunchNilConn(t *testing.T) {
	if err := Punch(nil, "127.0.0.1:1", 1, time.Millisecond); err == nil {
		t.Fatal("expected error for nil conn")
	}
}

func TestReflexiveAddr(t *testing.T) {
	// Network-dependent: skip gracefully when offline or the STUN server is
	// unreachable inside the sandbox.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	addr, err := ReflexiveAddr(ctx, "stun.l.google.com:19302")
	if err != nil {
		t.Skipf("skipping STUN test (no network?): %v", err)
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		t.Fatalf("reflexive addr %q not ip:port: %v", addr, err)
	}
	t.Logf("reflexive address: %s", addr)
}

func TestReflexiveAddrFromReusesConn(t *testing.T) {
	conn, err := net.ListenUDP("udp", nil)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	addr, err := ReflexiveAddrFrom(ctx, conn, "stun.l.google.com:19302")
	if err != nil {
		t.Skipf("skipping STUN test (no network?): %v", err)
	}
	if _, _, err := net.SplitHostPort(addr); err != nil {
		t.Fatalf("reflexive addr %q not ip:port: %v", addr, err)
	}

	// The conn must still be usable (deadline cleared) after discovery.
	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Millisecond)); err != nil {
		t.Fatalf("conn unusable after discovery: %v", err)
	}
}
