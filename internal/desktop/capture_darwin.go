//go:build darwin

package desktop

import (
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"os/exec"
)

// ErrCaptureUnavailable is returned by NewCapturer when no real screen-capture
// backend can be initialised. The host treats it as the signal to fall back to
// NewSyntheticCapturer.
var ErrCaptureUnavailable = errors.New(
	"desktop: screen capture not available on this system")

// macCapturer captures the macOS screen without cgo by shelling out to the
// built-in /usr/sbin/screencapture utility. Each Capture writes a PNG to a
// temporary file (-x silences the shutter sound, -t png selects the format) and
// decodes it into an image.RGBA.
//
// This is a real capture of the live macOS display. The first invocation may
// require the Screen Recording permission to be granted to the host process;
// without it screencapture produces a blank or denied image, which is a system
// policy matter rather than a code limitation.
type macCapturer struct {
	w, h int // learned from the first captured frame
}

// NewCapturer returns a screencapture-backed macOS capturer. It takes one probe
// screenshot so Bounds is known up front; if screencapture is missing or fails
// it returns ErrCaptureUnavailable.
func NewCapturer() (Capturer, error) {
	if _, err := os.Stat("/usr/sbin/screencapture"); err != nil {
		return nil, fmt.Errorf("%w: screencapture not found: %v", ErrCaptureUnavailable, err)
	}
	c := &macCapturer{}
	img, err := c.grab()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCaptureUnavailable, err)
	}
	b := img.Bounds()
	c.w, c.h = b.Dx(), b.Dy()
	return c, nil
}

func (c *macCapturer) Bounds() (int, int) { return c.w, c.h }

func (c *macCapturer) Capture() (*image.RGBA, error) {
	img, err := c.grab()
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	c.w, c.h = b.Dx(), b.Dy()
	return img, nil
}

func (c *macCapturer) grab() (*image.RGBA, error) {
	tmp, err := os.CreateTemp("", "remolo-screencap-*.png")
	if err != nil {
		return nil, fmt.Errorf("desktop: screencapture tempfile: %w", err)
	}
	name := tmp.Name()
	tmp.Close()
	defer os.Remove(name)

	cmd := exec.Command("/usr/sbin/screencapture", "-x", "-t", "png", name)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("desktop: screencapture: %w (%s)", err, string(out))
	}

	f, err := os.Open(name)
	if err != nil {
		return nil, fmt.Errorf("desktop: open screencapture file: %w", err)
	}
	src, err := png.Decode(f)
	f.Close()
	if err != nil {
		return nil, fmt.Errorf("desktop: decode screencapture png: %w", err)
	}

	if rgba, ok := src.(*image.RGBA); ok {
		return rgba, nil
	}
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Src)
	return dst, nil
}

func (c *macCapturer) Close() error { return nil }
