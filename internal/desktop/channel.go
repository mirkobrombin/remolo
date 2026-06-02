package desktop

import (
	"encoding/json"
	"fmt"
	"image"
	"io"
	"sync"
	"time"

	"github.com/mirkobrombin/remolo/internal/protocol"
)

// defaultFPS is used when a Select asks for a non-positive frame rate.
const defaultFPS = 10

// Serve runs the server side of a desktop channel over an already-open stream
// (the Open frame has been consumed by the caller). It advertises the available
// modes and screen size in a Hello, waits for the client's Select, then streams
// encoded frames at the negotiated rate while concurrently applying input events
// the client sends. A new Select frame at any time renegotiates mode/FPS.
//
// Serve returns when the stream errors or closes; it writes a final Exit frame.
func Serve(stream io.ReadWriter, cap Capturer, inj Injector) (err error) {
	if inj == nil {
		inj = NopInjector{}
	}
	w, h := cap.Bounds()
	// ModeWebRTC is advertised first as the preferred rung; ModeScreenshot and
	// ModeDiff remain as fallbacks the client may select instead.
	hello := Hello{Modes: []Mode{ModeWebRTC, ModeScreenshot, ModeDiff}, W: w, H: h}
	if err := protocol.WriteJSON(stream, hello); err != nil {
		return fmt.Errorf("desktop: write hello: %w", err)
	}

	// Wait for the initial Select.
	sel, err := readSelect(stream)
	if err != nil {
		writeExit(stream, err)
		return err
	}

	// WebRTC takes over the stream for signaling and then carries media on its own
	// data channel, so dispatch before the screenshot/diff loop.
	if sel.Mode == ModeWebRTC {
		werr := serveWebRTC(stream, cap, inj)
		writeExit(stream, werr)
		return werr
	}

	var (
		mu      sync.Mutex
		current = sel
		closed  bool
	)

	// Reader goroutine: handles input events and renegotiation Selects.
	readErr := make(chan error, 1)
	go func() {
		readErr <- serveReadLoop(stream, inj, &mu, &current)
	}()

	enc := NewEncoder(current.Mode)
	encMode := current.Mode

	defer func() {
		mu.Lock()
		closed = true
		mu.Unlock()
		_ = closed
		writeExit(stream, err)
	}()

	ticker := newRateTicker(current.FPS)
	defer ticker.Stop()

	for {
		select {
		case rerr := <-readErr:
			// Client closed its write half or errored. Clean shutdown.
			if rerr == io.EOF || rerr == nil {
				return nil
			}
			return rerr
		case <-ticker.C:
		}

		mu.Lock()
		sel := current
		mu.Unlock()

		if sel.Mode != encMode {
			enc.SetMode(sel.Mode)
			encMode = sel.Mode
		}
		ticker.SetFPS(sel.FPS)

		img, cerr := cap.Capture()
		if cerr != nil {
			return fmt.Errorf("desktop: capture: %w", cerr)
		}
		payload, eerr := enc.Encode(img)
		if eerr != nil {
			return eerr
		}
		if werr := protocol.WriteFrame(stream, protocol.FrameData, payload); werr != nil {
			return werr
		}
	}
}

func serveReadLoop(stream io.Reader, inj Injector, mu *sync.Mutex, current *Select) error {
	for {
		t, payload, err := protocol.ReadFrame(stream)
		if err != nil {
			return err
		}
		switch t {
		case protocol.FrameJSON:
			// Could be a renegotiation Select or an InputEvent. Distinguish by
			// trying Select first (it has a "mode" field).
			var probe struct {
				Mode Mode   `json:"mode"`
				Type string `json:"type"`
			}
			if json.Unmarshal(payload, &probe) == nil && probe.Mode != "" {
				var s Select
				if json.Unmarshal(payload, &s) == nil {
					if s.FPS <= 0 {
						s.FPS = defaultFPS
					}
					mu.Lock()
					*current = s
					mu.Unlock()
				}
				continue
			}
			var ev InputEvent
			if json.Unmarshal(payload, &ev) == nil {
				applyInput(inj, ev)
			}
		case protocol.FrameExit, protocol.FrameEOF:
			return io.EOF
		}
	}
}

func applyInput(inj Injector, ev InputEvent) {
	switch ev.Type {
	case "mousemove":
		_ = inj.MouseMove(ev.X, ev.Y)
	case "mousebutton":
		_ = inj.MouseButton(ev.Button, ev.Down)
	case "key":
		_ = inj.Key(ev.Key, ev.Down)
	case "wheel":
		_ = inj.Scroll(ev.Dy)
	}
}

// Run is the client side of a desktop channel. It reads the server's Hello,
// sends a Select for the requested mode/FPS, then decodes incoming frames into a
// framebuffer, invoking onFrame for each decoded frame. Input events received on
// inputs are forwarded to the server as FrameJSON frames. Run returns when the
// stream closes or an Exit frame arrives.
func Run(stream io.ReadWriter, mode Mode, fps int, onFrame func(*image.RGBA), inputs <-chan InputEvent) error {
	// Read Hello.
	if _, err := readHello(stream); err != nil {
		return err
	}
	if fps <= 0 {
		fps = defaultFPS
	}
	if err := protocol.WriteJSON(stream, Select{Mode: mode, FPS: fps}); err != nil {
		return fmt.Errorf("desktop: write select: %w", err)
	}

	// WebRTC owns the stream (signaling) and its own data channel (media + input),
	// so dispatch before setting up the screenshot/diff read loop.
	if mode == ModeWebRTC {
		return runWebRTC(stream, fps, onFrame, inputs)
	}

	// Forward input events until inputs is closed or the session ends.
	done := make(chan struct{})
	var fwdWG sync.WaitGroup
	if inputs != nil {
		fwdWG.Add(1)
		go func() {
			defer fwdWG.Done()
			for {
				select {
				case <-done:
					return
				case ev, ok := <-inputs:
					if !ok {
						return
					}
					_ = protocol.WriteJSON(stream, ev)
				}
			}
		}()
	}

	dec := NewDecoder()
	defer func() {
		close(done)
		fwdWG.Wait()
	}()

	for {
		t, payload, err := protocol.ReadFrame(stream)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		switch t {
		case protocol.FrameData:
			img, derr := dec.Decode(payload)
			if derr != nil {
				return derr
			}
			if onFrame != nil {
				onFrame(img)
			}
		case protocol.FrameExit:
			return nil
		case protocol.FrameEOF:
			return nil
		}
	}
}

// --- negotiation helpers ---

func readHello(r io.Reader) (Hello, error) {
	t, payload, err := protocol.ReadFrame(r)
	if err != nil {
		return Hello{}, err
	}
	if t != protocol.FrameJSON {
		return Hello{}, fmt.Errorf("desktop: expected hello, got frame type %d", t)
	}
	var hl Hello
	if err := json.Unmarshal(payload, &hl); err != nil {
		return Hello{}, fmt.Errorf("desktop: decode hello: %w", err)
	}
	return hl, nil
}

func readSelect(r io.Reader) (Select, error) {
	t, payload, err := protocol.ReadFrame(r)
	if err != nil {
		return Select{}, err
	}
	if t != protocol.FrameJSON {
		return Select{}, fmt.Errorf("desktop: expected select, got frame type %d", t)
	}
	var s Select
	if err := json.Unmarshal(payload, &s); err != nil {
		return Select{}, fmt.Errorf("desktop: decode select: %w", err)
	}
	if s.Mode == "" {
		return Select{}, fmt.Errorf("desktop: select missing mode")
	}
	if s.FPS <= 0 {
		s.FPS = defaultFPS
	}
	return s, nil
}

func writeExit(w io.Writer, err error) {
	e := protocol.Exit{}
	if err != nil && err != io.EOF {
		e.Code = 1
		e.Err = err.Error()
	}
	_ = protocol.WriteExit(w, e)
}

// rateTicker is a ticker whose interval can be changed at runtime to follow FPS
// renegotiation.
type rateTicker struct {
	C   <-chan time.Time
	t   *time.Ticker
	fps int
}

func newRateTicker(fps int) *rateTicker {
	if fps <= 0 {
		fps = defaultFPS
	}
	t := time.NewTicker(time.Second / time.Duration(fps))
	return &rateTicker{C: t.C, t: t, fps: fps}
}

func (r *rateTicker) SetFPS(fps int) {
	if fps <= 0 {
		fps = defaultFPS
	}
	if fps == r.fps {
		return
	}
	r.fps = fps
	r.t.Reset(time.Second / time.Duration(fps))
}

func (r *rateTicker) Stop() { r.t.Stop() }
