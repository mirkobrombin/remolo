package transport

import (
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/mirkobrombin/remolo/internal/crypto"
)

// echoServer accepts one connection, then echoes every stream it receives.
func echoServer(t *testing.T, l *Listener) {
	t.Helper()
	go func() {
		conn, err := l.Accept(context.Background())
		if err != nil {
			return
		}
		for {
			s, err := conn.AcceptStream(context.Background())
			if err != nil {
				return
			}
			go func(s Stream) {
				io.Copy(s, s)
				s.Close()
			}(s)
		}
	}()
}

func TestQUICDialAndEcho(t *testing.T) {
	id, err := crypto.GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	l, err := ListenQUIC("127.0.0.1:0", id.ServerTLSConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	echoServer(t, l)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	addr := fmt.Sprintf("127.0.0.1:%d", l.Port())
	conn, err := DialQUIC(ctx, addr, crypto.ClientTLSConfig(id.PublicKey()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close("done")

	s, err := conn.OpenStream(ctx)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	msg := []byte("hello remolo")
	if _, err := s.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(s, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != string(msg) {
		t.Fatalf("echo mismatch: %q", buf)
	}
}

func TestDialWrongPinFails(t *testing.T) {
	id, _ := crypto.GenerateIdentity()
	other, _ := crypto.GenerateIdentity()
	l, err := ListenQUIC("127.0.0.1:0", id.ServerTLSConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	echoServer(t, l)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	addr := fmt.Sprintf("127.0.0.1:%d", l.Port())
	// Pin to the wrong key: the TLS verify callback must reject the host.
	if _, err := DialQUIC(ctx, addr, crypto.ClientTLSConfig(other.PublicKey())); err == nil {
		t.Fatal("dial with wrong pin should fail")
	}
}

func TestRacePicksReachable(t *testing.T) {
	id, _ := crypto.GenerateIdentity()
	l, err := ListenQUIC("127.0.0.1:0", id.ServerTLSConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	echoServer(t, l)

	good := fmt.Sprintf("127.0.0.1:%d", l.Port())
	tlsConf := crypto.ClientTLSConfig(id.PublicKey())

	attempts := []Attempt{
		// An unreachable endpoint, ranked best, must lose to the reachable one.
		{Label: "dead", Quality: QualityDirect, Dial: func(ctx context.Context) (Conn, error) {
			return DialQUIC(ctx, "127.0.0.1:1", tlsConf)
		}},
		{Label: "alive", Quality: QualityDirect, Dial: func(ctx context.Context) (Conn, error) {
			return DialQUIC(ctx, good, tlsConf)
		}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	var seen []Result
	conn, label, err := Race(ctx, attempts, 50*time.Millisecond, func(r Result) { seen = append(seen, r) })
	if err != nil {
		t.Fatalf("race: %v", err)
	}
	defer conn.Close("done")
	if label != "alive" {
		t.Fatalf("expected 'alive' to win, got %q", label)
	}
	if len(seen) == 0 {
		t.Fatal("expected attempt results to be reported")
	}
}
