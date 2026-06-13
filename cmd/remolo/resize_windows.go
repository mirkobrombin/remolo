//go:build windows

package main

import (
	"time"

	"github.com/mirkobrombin/remolo/internal/protocol"
	"golang.org/x/term"
)

// installResizeHandler polls the terminal size on Windows (which has no
// SIGWINCH) and forwards changes to the resize channel. It returns a stopper.
func installResizeHandler(fd int, resize chan<- protocol.Resize) func() {
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		lastW, lastH := -1, -1
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				w, h, err := term.GetSize(fd)
				if err != nil || (w == lastW && h == lastH) {
					continue
				}
				lastW, lastH = w, h
				select {
				case resize <- protocol.Resize{Cols: uint16(w), Rows: uint16(h)}:
				default:
				}
			}
		}
	}()
	return func() { close(done) }
}
