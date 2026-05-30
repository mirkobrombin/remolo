// Package desktop implements remolo's remote-desktop subsystem: screen capture,
// frame encoding, and input injection, all in pure Go with no cgo.
//
// The subsystem is built as a "degradation ladder". Each rung is a capture and
// encoding mode the two peers negotiate. From best to most portable:
//
//	ModeWebRTC     frames and input over an SCTP data channel (pion)
//	ModeDiff       tile diffing: only changed 64x64 tiles are sent
//	ModeScreenshot full frames at a chosen frame rate
//
// Capture is real on every supported platform and stays cgo-free: X11 via the
// wire protocol, Wayland via the xdg-desktop-portal over D-Bus, Windows via
// GDI, macOS via the built-in screencapture utility. When no display is
// reachable (a headless box, a denied portal) NewCapturer reports
// ErrCaptureUnavailable and the host falls back to NewSyntheticCapturer, so a
// session degrades instead of dying.
//
// Input injection is real on Linux through /dev/uinput (NewUinputInjector),
// degrading to an "unsupported" stub elsewhere.
package desktop

import "image"

// Image is a simple RGBA framebuffer. The codecs operate on image.RGBA from the
// standard library; this alias documents the byte layout the wire format uses:
// Pix is row-major RGBA, 4 bytes per pixel, W*4 bytes per row.
type Image struct {
	W, H int
	Pix  []byte
}

// Capturer produces frames of the remote screen.
type Capturer interface {
	// Capture returns the current screen as an RGBA image. Successive calls may
	// reuse the same backing buffer, so callers that retain a frame across calls
	// must copy it.
	Capture() (*image.RGBA, error)
	// Bounds reports the capture dimensions in pixels.
	Bounds() (w, h int)
	// Close releases any resources held by the capturer.
	Close() error
}

// Injector applies remote input events to the local machine.
type Injector interface {
	// MouseMove moves the pointer to absolute coordinates (x, y).
	MouseMove(x, y int) error
	// MouseButton presses (down=true) or releases (down=false) a button. The
	// button is a Linux BTN_* code (e.g. BtnLeft).
	MouseButton(button int, down bool) error
	// Key presses (down=true) or releases (down=false) a key by Linux keycode.
	Key(keycode int, down bool) error
	// Scroll emits a vertical wheel movement (positive scrolls up).
	Scroll(dy int) error
	// Close releases the injector and any virtual device it created.
	Close() error
}

// Mode names a rung on the desktop degradation ladder.
type Mode string

const (
	// ModeScreenshot sends a full-frame PNG per captured frame. Simplest and
	// most robust; highest bandwidth.
	ModeScreenshot Mode = "screenshot"
	// ModeDiff splits each frame into tiles and sends only the tiles that
	// changed since the previous frame.
	ModeDiff Mode = "diff"
	// ModeWebRTC carries frames and input over a WebRTC data channel, with SDP
	// signaling on the desktop stream itself. The host advertises it first; a
	// client opts in by selecting it.
	ModeWebRTC Mode = "webrtc"
)

// Hello is the server's opening negotiation message: the modes it can serve and
// the screen size.
type Hello struct {
	Modes []Mode `json:"modes"`
	W     int    `json:"w"`
	H     int    `json:"h"`
}

// Select is the client's choice of mode and target frame rate.
type Select struct {
	Mode Mode `json:"mode"`
	FPS  int  `json:"fps"`
}

// InputEvent is a single input action forwarded from client to server. Type is
// one of "mousemove", "mousebutton", or "key".
type InputEvent struct {
	Type   string `json:"type"`
	X      int    `json:"x,omitempty"`
	Y      int    `json:"y,omitempty"`
	Button int    `json:"button,omitempty"`
	Key    int    `json:"key,omitempty"`
	Down   bool   `json:"down,omitempty"`
	Dy     int    `json:"dy,omitempty"` // wheel delta (scroll)
}

// Common Linux button codes, re-exported for callers that build InputEvents.
const (
	BtnLeft   = 0x110 // BTN_LEFT
	BtnRight  = 0x111 // BTN_RIGHT
	BtnMiddle = 0x112 // BTN_MIDDLE
)
