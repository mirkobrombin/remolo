package webterm

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mirkobrombin/remolo/internal/protocol"
)

// fakeHost runs the remote end of a PTY channel over one side of a net.Pipe.
// It echoes "hello\n" as "echo:hello\n" so the test can assert round-trip flow.
func fakeHost(t *testing.T, conn net.Conn) {
	t.Helper()
	defer conn.Close()
	for {
		ft, payload, err := protocol.ReadFrame(conn)
		if err != nil {
			return
		}
		switch ft {
		case protocol.FrameData:
			if string(payload) == "hello\n" {
				if werr := protocol.WriteFrame(conn, protocol.FrameData, []byte("echo:hello\n")); werr != nil {
					return
				}
			}
		case protocol.FrameResize:
			// Surface the resize so the test goroutine can observe it.
			var rs protocol.Resize
			if json.Unmarshal(payload, &rs) == nil {
				select {
				case resizeSeen <- rs:
				default:
				}
			}
		}
	}
}

// resizeSeen reports FrameResize values received by the fake host.
var resizeSeen = make(chan protocol.Resize, 4)

func TestServeRoundTrip(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	open := func(cols, rows uint16) (io.ReadWriteCloser, error) {
		client, host := net.Pipe()
		go fakeHost(t, host)
		return client, nil
	}

	url, wait, err := Serve(ctx, Options{Addr: "127.0.0.1:0"}, open)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	defer func() {
		cancel()
		_ = wait()
	}()

	base := strings.TrimSuffix(url, "/")

	// Open the SSE output stream.
	outReq, _ := http.NewRequest(http.MethodGet, base+"/output?cols=80&rows=24", nil)
	client := &http.Client{Timeout: 10 * time.Second}
	outResp, err := client.Do(outReq)
	if err != nil {
		t.Fatalf("GET /output: %v", err)
	}
	defer outResp.Body.Close()
	if outResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /output status = %d", outResp.StatusCode)
	}
	if ct := outResp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("GET /output content-type = %q", ct)
	}

	// POST a resize and confirm the host received a FrameResize.
	rs := protocol.Resize{Cols: 100, Rows: 40}
	rsBody, _ := json.Marshal(rs)
	resizeResp, err := client.Post(base+"/resize", "application/json", strings.NewReader(string(rsBody)))
	if err != nil {
		t.Fatalf("POST /resize: %v", err)
	}
	resizeResp.Body.Close()
	if resizeResp.StatusCode != http.StatusNoContent {
		t.Fatalf("POST /resize status = %d", resizeResp.StatusCode)
	}
	select {
	case got := <-resizeSeen:
		if got != rs {
			t.Fatalf("resize: got %+v want %+v", got, rs)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for FrameResize at host")
	}

	// POST input "hello\n" and read the echoed SSE event.
	inResp, err := client.Post(base+"/input", "application/octet-stream", strings.NewReader("hello\n"))
	if err != nil {
		t.Fatalf("POST /input: %v", err)
	}
	inResp.Body.Close()
	if inResp.StatusCode != http.StatusNoContent {
		t.Fatalf("POST /input status = %d", inResp.StatusCode)
	}

	got := readSSEData(t, outResp.Body, "echo:hello\n", 5*time.Second)
	if got != "echo:hello\n" {
		t.Fatalf("SSE data: got %q want %q", got, "echo:hello\n")
	}
}

// readSSEData scans an SSE stream for a "data:" line whose base64 decodes to
// want, returning the decoded payload. It fails the test on timeout.
func readSSEData(t *testing.T, body io.Reader, want string, timeout time.Duration) string {
	t.Helper()
	type result struct {
		s   string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		sc := bufio.NewScanner(body)
		for sc.Scan() {
			line := sc.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(line, "data: "))
			if err != nil {
				ch <- result{err: err}
				return
			}
			if string(raw) == want {
				ch <- result{s: string(raw)}
				return
			}
		}
		ch <- result{err: io.EOF}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("readSSEData: %v", r.err)
		}
		return r.s
	case <-time.After(timeout):
		t.Fatal("readSSEData: timed out")
		return ""
	}
}

func TestServeRequiresOpen(t *testing.T) {
	_, _, err := Serve(context.Background(), Options{}, nil)
	if err == nil {
		t.Fatal("expected error when OpenPTY is nil")
	}
}

func TestParseDim(t *testing.T) {
	cases := []struct {
		in  string
		def uint16
		out uint16
	}{
		{"", 24, 24},
		{"80", 24, 80},
		{"0", 24, 24},
		{"-5", 24, 24},
		{"abc", 24, 24},
		{"70000", 24, 24},
		{"65535", 24, 65535},
	}
	for _, c := range cases {
		if got := parseDim(c.in, c.def); got != c.out {
			t.Errorf("parseDim(%q, %d) = %d want %d", c.in, c.def, got, c.out)
		}
	}
}
