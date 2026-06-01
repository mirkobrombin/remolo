package desktop

import (
	"testing"
)

// TestRealCapture exercises the real per-OS NewCapturer. In a headless sandbox
// (no X11/Wayland display, no GDI, no screencapture permission) it skips rather
// than fails: the host's contract is to fall back to synthetic capture there. On
// a machine with a usable display it asserts non-empty bounds and a decodable
// frame whose pixel buffer matches those bounds.
func TestRealCapture(t *testing.T) {
	c, err := NewCapturer()
	if err != nil {
		t.Skipf("real capture unavailable (expected in headless CI): %v", err)
	}
	defer c.Close()

	w, h := c.Bounds()
	if w <= 0 || h <= 0 {
		t.Fatalf("bounds non-positive: %dx%d", w, h)
	}

	img, err := c.Capture()
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if img == nil {
		t.Fatal("capture returned nil image")
	}
	b := img.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		t.Fatalf("captured frame empty: %v", b)
	}
	// The RGBA buffer must hold 4 bytes per pixel for the frame's area.
	if len(img.Pix) < b.Dx()*b.Dy()*4 {
		t.Fatalf("pix buffer too small: have %d want >= %d", len(img.Pix), b.Dx()*b.Dy()*4)
	}

	// A second capture must also succeed (resources are reused, not leaked).
	if _, err := c.Capture(); err != nil {
		t.Fatalf("second capture: %v", err)
	}
}
