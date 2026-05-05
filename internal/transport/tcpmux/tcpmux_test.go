package tcpmux

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"sync"
	"testing"
	"time"
)

// selfSignedTLS generates an in-memory self-signed certificate and returns a
// server config and a matching (insecure) client config for tests.
func selfSignedTLS(t *testing.T) (server, client *tls.Config) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "remolo-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}
	server = &tls.Config{Certificates: []tls.Certificate{cert}}
	client = &tls.Config{InsecureSkipVerify: true}
	return server, client
}

// echoServe accepts one transport.Conn and echoes every inbound stream.
func echoServe(t *testing.T, ln *Listener) {
	t.Helper()
	conn, err := ln.Accept(context.Background())
	if err != nil {
		return
	}
	for {
		s, err := conn.AcceptStream(context.Background())
		if err != nil {
			return
		}
		go func() {
			defer s.Close()
			io.Copy(s, s)
		}()
	}
}

func TestEchoRoundTrip(t *testing.T) {
	srvConf, cliConf := selfSignedTLS(t)
	ln, err := ListenTCP("127.0.0.1:0", srvConf)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go echoServe(t, ln)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := DialTCP(ctx, ln.Addr().String(), cliConf)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close("done")

	s, err := conn.OpenStream(ctx)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	msg := []byte("hello remolo over tcpmux")
	if _, err := s.Write(msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(s, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != string(msg) {
		t.Fatalf("got %q want %q", buf, msg)
	}
	s.Close()
}

func TestConcurrentStreams(t *testing.T) {
	srvConf, cliConf := selfSignedTLS(t)
	ln, err := ListenTCP("127.0.0.1:0", srvConf)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go echoServe(t, ln)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := DialTCP(ctx, ln.Addr().String(), cliConf)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close("done")

	const n = 16
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			s, err := conn.OpenStream(ctx)
			if err != nil {
				errs <- err
				return
			}
			defer s.Close()
			payload := make([]byte, 1024)
			for j := range payload {
				payload[j] = byte(i + j)
			}
			if _, err := s.Write(payload); err != nil {
				errs <- err
				return
			}
			got := make([]byte, len(payload))
			if _, err := io.ReadFull(s, got); err != nil {
				errs <- err
				return
			}
			for j := range payload {
				if got[j] != payload[j] {
					errs <- io.ErrUnexpectedEOF
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
	}
}
