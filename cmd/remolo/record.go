package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// castRecorder writes terminal output to an asciinema cast v2 file
// (https://docs.asciinema.org/manual/asciicast/v2/): a JSON header line
// followed by one [elapsed, "o", data] event per output chunk. It satisfies
// io.Writer so it can be tee'd off the shell's output stream.
type castRecorder struct {
	mu    sync.Mutex
	f     *os.File
	start time.Time
	enc   *json.Encoder
}

func newCastRecorder(path string, cols, rows uint16) (*castRecorder, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	header := map[string]any{
		"version":   2,
		"width":     cols,
		"height":    rows,
		"timestamp": time.Now().Unix(),
		"env":       map[string]string{"TERM": os.Getenv("TERM"), "SHELL": os.Getenv("SHELL")},
	}
	b, _ := json.Marshal(header)
	if _, err := fmt.Fprintf(f, "%s\n", b); err != nil {
		f.Close()
		return nil, err
	}
	return &castRecorder{f: f, start: time.Now(), enc: json.NewEncoder(f)}, nil
}

// Write records one output chunk as a timestamped asciinema event.
func (r *castRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	elapsed := time.Since(r.start).Seconds()
	// asciinema events are 3-element arrays: [time, code, data].
	if err := r.enc.Encode([]any{elapsed, "o", string(p)}); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (r *castRecorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f.Close()
}
