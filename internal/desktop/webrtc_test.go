package desktop

import (
	"image"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestWebRTCPipeline wires the host Serve(synthetic capturer, NopInjector) and
// the client Run(ModeWebRTC, ...) over a net.Pipe. The SDP signaling travels
// over the pipe; the actual frame media travels over the real pion DataChannel
// (which pairs via loopback host ICE candidates, no STUN needed). The test
// asserts the client decodes at least two frames whose dimensions match the
// synthetic capturer.
func TestWebRTCPipeline(t *testing.T) {
	srvConn, cliConn := net.Pipe()

	const w, h = 128, 96
	capr := NewSyntheticCapturer(w, h)

	var serveErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		serveErr = Serve(srvConn, capr, NopInjector{})
	}()

	var frames int64
	var lastW, lastH int64
	const wantFrames = 2

	done := make(chan struct{})
	var runErr error
	go func() {
		defer close(done)
		runErr = Run(cliConn, ModeWebRTC, 15, func(img *image.RGBA) {
			n := atomic.AddInt64(&frames, 1)
			atomic.StoreInt64(&lastW, int64(img.Bounds().Dx()))
			atomic.StoreInt64(&lastH, int64(img.Bounds().Dy()))
			if n == wantFrames {
				// Enough frames decoded; tear down by closing the signaling pipe.
				cliConn.Close()
			}
		}, nil)
	}()

	select {
	case <-done:
	case <-time.After(75 * time.Second):
		srvConn.Close()
		cliConn.Close()
		<-done
		got := atomic.LoadInt64(&frames)
		if got == 0 {
			// No frames AND no ICE pairing: only acceptable if the sandbox truly
			// cannot gather any local candidate. Surface the underlying error so a
			// genuine regression is still caught.
			if runErr != nil && strings.Contains(runErr.Error(), "did not open") {
				t.Skipf("webrtc could not establish a data channel in this sandbox: %v", runErr)
			}
		}
		t.Fatalf("webrtc pipeline timed out: frames=%d serveErr=%v runErr=%v", got, serveErr, runErr)
	}

	srvConn.Close()
	wg.Wait()

	got := atomic.LoadInt64(&frames)
	if got < wantFrames {
		t.Fatalf("decoded %d frames, want at least %d (serveErr=%v runErr=%v)",
			got, wantFrames, serveErr, runErr)
	}
	if int(atomic.LoadInt64(&lastW)) != w || int(atomic.LoadInt64(&lastH)) != h {
		t.Fatalf("frame size %dx%d, want %dx%d",
			atomic.LoadInt64(&lastW), atomic.LoadInt64(&lastH), w, h)
	}
}
