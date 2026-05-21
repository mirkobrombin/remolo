// Package rest exposes a small local HTTP adapter over the rpc control plane so
// scripts and health checks can reach a remolo host without speaking the framed
// protocol directly.
//
// Each request opens a fresh rpc channel via the supplied dial function, runs a
// single rpc.Call, and closes the stream. The mux is intentionally tiny: a
// health probe plus the two most useful rpc methods (sysinfo and exec).
package rest

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/mirkobrombin/remolo/internal/rpc"
)

// DialFunc opens a fresh stream to an rpc.Server on the remote host. The mux
// closes the returned stream when the request finishes.
type DialFunc func() (io.ReadWriteCloser, error)

// NewMux builds the HTTP handler. dial opens one rpc channel per request.
func NewMux(dial DialFunc) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", handleHealthz)
	mux.HandleFunc("/sysinfo", sysinfoHandler(dial))
	mux.HandleFunc("/exec", execHandler(dial))
	return mux
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, "ok")
}

func sysinfoHandler(dial DialFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		stream, err := dial()
		if err != nil {
			httpError(w, http.StatusBadGateway, "dial: "+err.Error())
			return
		}
		defer stream.Close()

		var info rpc.SysInfo
		if err := rpc.Call(stream, "sysinfo", nil, &info); err != nil {
			httpError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, info)
	}
}

func execHandler(dial DialFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		var params rpc.ExecParams
		if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
			httpError(w, http.StatusBadRequest, "decode body: "+err.Error())
			return
		}
		if len(params.Cmd) == 0 {
			httpError(w, http.StatusBadRequest, "cmd is required")
			return
		}

		stream, err := dial()
		if err != nil {
			httpError(w, http.StatusBadGateway, "dial: "+err.Error())
			return
		}
		defer stream.Close()

		var result rpc.ExecResult
		if err := rpc.Call(stream, "exec", params, &result); err != nil {
			httpError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	httpError(w, http.StatusMethodNotAllowed, "method not allowed")
}
