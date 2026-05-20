package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"runtime"
	"testing"
)

func TestSysInfoRoundTrip(t *testing.T) {
	c, s := net.Pipe()
	srv := DefaultServer()
	srvErr := make(chan error, 1)
	go func() { srvErr <- srv.Serve(s) }()

	var info SysInfo
	if err := Call(c, "sysinfo", nil, &info); err != nil {
		t.Fatalf("Call(sysinfo): %v", err)
	}
	if info.OS != runtime.GOOS {
		t.Fatalf("OS = %q, want %q", info.OS, runtime.GOOS)
	}
	if info.Arch != runtime.GOARCH {
		t.Fatalf("Arch = %q, want %q", info.Arch, runtime.GOARCH)
	}
	if info.NumCPU != runtime.NumCPU() {
		t.Fatalf("NumCPU = %d, want %d", info.NumCPU, runtime.NumCPU())
	}
	c.Close()
	<-srvErr
}

func TestExecRoundTrip(t *testing.T) {
	if _, err := lookEcho(); err != nil {
		t.Skipf("echo not available: %v", err)
	}
	c, s := net.Pipe()
	srv := DefaultServer()
	srvErr := make(chan error, 1)
	go func() { srvErr <- srv.Serve(s) }()

	var res ExecResult
	if err := Call(c, "exec", ExecParams{Cmd: []string{"/bin/echo", "hi"}}, &res); err != nil {
		t.Fatalf("Call(exec): %v", err)
	}
	if res.Code != 0 {
		t.Fatalf("exit code = %d, want 0", res.Code)
	}
	if res.Stdout != "hi\n" {
		t.Fatalf("stdout = %q, want %q", res.Stdout, "hi\n")
	}
	c.Close()
	<-srvErr
}

func TestClientSequentialCalls(t *testing.T) {
	c, s := net.Pipe()
	srv := DefaultServer()
	srvErr := make(chan error, 1)
	go func() { srvErr <- srv.Serve(s) }()

	cl := NewClient(c)
	var a, b SysInfo
	if err := cl.Call("sysinfo", nil, &a); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := cl.Call("sysinfo", nil, &b); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if a.OS != b.OS {
		t.Fatalf("inconsistent results across sequential calls")
	}
	c.Close()
	<-srvErr
}

func TestUnknownMethod(t *testing.T) {
	c, s := net.Pipe()
	srv := NewServer()
	srvErr := make(chan error, 1)
	go func() { srvErr <- srv.Serve(s) }()

	err := Call(c, "nope", nil, nil)
	if err == nil {
		t.Fatalf("expected error for unknown method")
	}
	c.Close()
	<-srvErr
}

func TestHandlerError(t *testing.T) {
	c, s := net.Pipe()
	srv := NewServer()
	srv.Register("fail", func(_ context.Context, _ json.RawMessage) (any, error) {
		return nil, errors.New("boom")
	})
	srvErr := make(chan error, 1)
	go func() { srvErr <- srv.Serve(s) }()

	err := Call(c, "fail", nil, nil)
	if err == nil {
		t.Fatalf("expected handler error to propagate")
	}
	c.Close()
	<-srvErr
}

func lookEcho() (string, error) {
	// /bin/echo is expected on the unix test hosts used here.
	if runtime.GOOS == "windows" {
		return "", errors.New("no /bin/echo on windows")
	}
	return "/bin/echo", nil
}
