//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/mirkobrombin/remolo/internal/protocol"
	"golang.org/x/term"
)

// installResizeHandler forwards SIGWINCH-driven terminal resizes to the resize
// channel and returns a stopper.
func installResizeHandler(fd int, resize chan<- protocol.Resize) func() {
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	go func() {
		for range winch {
			if cols, rows, err := term.GetSize(fd); err == nil {
				select {
				case resize <- protocol.Resize{Cols: uint16(cols), Rows: uint16(rows)}:
				default:
				}
			}
		}
	}()
	return func() { signal.Stop(winch); close(winch) }
}
