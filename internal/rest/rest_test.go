package rest

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"

	"github.com/mirkobrombin/remolo/internal/rpc"
)

// fakeDial returns a DialFunc whose streams are wired to a fresh rpc.DefaultServer
// goroutine via net.Pipe, mimicking a real channel to the host.
func fakeDial() DialFunc {
	return func() (io.ReadWriteCloser, error) {
		clientEnd, serverEnd := net.Pipe()
		srv := rpc.DefaultServer()
		go func() {
			_ = srv.Serve(serverEnd)
			_ = serverEnd.Close()
		}()
		return clientEnd, nil
	}
}

func TestHealthz(t *testing.T) {
	mux := NewMux(fakeDial())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Fatalf("body = %q, want %q", rec.Body.String(), "ok")
	}
}

func TestSysInfoEndpoint(t *testing.T) {
	mux := NewMux(fakeDial())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sysinfo", nil)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var info rpc.SysInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if info.OS != runtime.GOOS {
		t.Fatalf("OS = %q, want %q", info.OS, runtime.GOOS)
	}
}

func TestExecEndpoint(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no /bin/echo on windows")
	}
	mux := NewMux(fakeDial())
	rec := httptest.NewRecorder()
	body := bytes.NewBufferString(`{"cmd":["/bin/echo","hi"]}`)
	req := httptest.NewRequest(http.MethodPost, "/exec", body)
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var res rpc.ExecResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Code != 0 {
		t.Fatalf("exit code = %d, want 0", res.Code)
	}
	if res.Stdout != "hi\n" {
		t.Fatalf("stdout = %q, want %q", res.Stdout, "hi\n")
	}
}

func TestExecBadBody(t *testing.T) {
	mux := NewMux(fakeDial())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/exec", bytes.NewBufferString("not json"))
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	mux := NewMux(fakeDial())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/sysinfo", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}
