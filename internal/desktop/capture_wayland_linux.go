//go:build linux

package desktop

import (
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// waylandCapturer captures the screen on Wayland sessions, where there is no X
// server to grab. It is cgo-free: it talks to xdg-desktop-portal over the
// session D-Bus (github.com/godbus/dbus/v5), asking the portal's Screenshot
// interface for a PNG of the screen and decoding that file.
//
// Portals are deliberately slower than a raw framebuffer grab (the compositor
// renders and writes a PNG per request), and on some compositors a screenshot
// may prompt the user the first time. That is the honest trade for a cgo-free,
// compositor-agnostic Wayland path. A wlroots `grim` fallback is used when the
// portal is unavailable.
type waylandCapturer struct {
	conn    *dbus.Conn
	w, h    int // learned from the first captured frame
	useGrim bool
}

// portal D-Bus names.
const (
	portalDest        = "org.freedesktop.portal.Desktop"
	portalPath        = "/org/freedesktop/portal/desktop"
	portalScreenshot  = "org.freedesktop.portal.Screenshot.Screenshot"
	portalRequestIf   = "org.freedesktop.portal.Request"
	portalResponseSig = "Response"
)

// newWaylandCapturer prepares a portal-backed capturer. It connects to the
// session bus and verifies a screenshot can be taken; if neither the portal nor
// grim can produce a frame it returns ErrCaptureUnavailable so the host falls
// back to synthetic capture.
func newWaylandCapturer() (Capturer, error) {
	c := &waylandCapturer{}
	conn, err := dbus.SessionBus()
	if err == nil {
		c.conn = conn
		// Probe once so Bounds is known and we fail fast on headless sessions.
		img, perr := c.portalScreenshot()
		if perr == nil {
			b := img.Bounds()
			c.w, c.h = b.Dx(), b.Dy()
			return c, nil
		}
		// Portal unavailable: fall through to grim probe.
	}

	if _, lerr := exec.LookPath("grim"); lerr == nil {
		img, gerr := grimScreenshot()
		if gerr == nil {
			b := img.Bounds()
			c.useGrim = true
			c.w, c.h = b.Dx(), b.Dy()
			return c, nil
		}
	}

	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
	return nil, fmt.Errorf("%w: no working wayland screenshot backend", ErrCaptureUnavailable)
}

func (c *waylandCapturer) Bounds() (int, int) { return c.w, c.h }

func (c *waylandCapturer) Capture() (*image.RGBA, error) {
	var (
		img *image.RGBA
		err error
	)
	if c.useGrim {
		img, err = grimScreenshot()
	} else {
		img, err = c.portalScreenshot()
	}
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	c.w, c.h = b.Dx(), b.Dy()
	return img, nil
}

func (c *waylandCapturer) Close() error {
	if c.conn != nil {
		err := c.conn.Close()
		c.conn = nil
		return err
	}
	return nil
}

// portalScreenshot performs one org.freedesktop.portal.Screenshot.Screenshot
// call and waits for the Request object's Response signal, which carries the
// "uri" of the resulting PNG.
func (c *waylandCapturer) portalScreenshot() (*image.RGBA, error) {
	if c.conn == nil {
		return nil, errors.New("desktop: wayland portal: no session bus")
	}

	// Subscribe to all Response signals from the portal before issuing the call,
	// so we cannot miss a fast reply. We match the request path once we have it.
	sigCh := make(chan *dbus.Signal, 4)
	c.conn.Signal(sigCh)
	defer c.conn.RemoveSignal(sigCh)

	if err := c.conn.AddMatchSignal(
		dbus.WithMatchInterface(portalRequestIf),
		dbus.WithMatchMember(portalResponseSig),
	); err != nil {
		return nil, fmt.Errorf("desktop: wayland portal: add match: %w", err)
	}
	defer c.conn.RemoveMatchSignal(
		dbus.WithMatchInterface(portalRequestIf),
		dbus.WithMatchMember(portalResponseSig),
	)

	obj := c.conn.Object(portalDest, dbus.ObjectPath(portalPath))
	options := map[string]dbus.Variant{
		"interactive": dbus.MakeVariant(false),
	}
	var requestPath dbus.ObjectPath
	call := obj.Call(portalScreenshot, 0, "", options)
	if call.Err != nil {
		return nil, fmt.Errorf("desktop: wayland portal: call: %w", call.Err)
	}
	if err := call.Store(&requestPath); err != nil {
		return nil, fmt.Errorf("desktop: wayland portal: store request path: %w", err)
	}

	deadline := time.After(15 * time.Second)
	for {
		select {
		case sig := <-sigCh:
			if sig == nil || sig.Path != requestPath {
				continue
			}
			if len(sig.Body) < 2 {
				return nil, errors.New("desktop: wayland portal: malformed response")
			}
			code, _ := sig.Body[0].(uint32)
			if code != 0 {
				return nil, fmt.Errorf("desktop: wayland portal: response code %d", code)
			}
			results, _ := sig.Body[1].(map[string]dbus.Variant)
			uriVar, ok := results["uri"]
			if !ok {
				return nil, errors.New("desktop: wayland portal: response missing uri")
			}
			uri, _ := uriVar.Value().(string)
			path, err := fileURIToPath(uri)
			if err != nil {
				return nil, err
			}
			return decodePNGFile(path, true)
		case <-deadline:
			return nil, errors.New("desktop: wayland portal: timed out waiting for response")
		}
	}
}

// grimScreenshot shells out to the wlroots `grim` tool, capturing the whole
// output to a temporary PNG which is then decoded.
func grimScreenshot() (*image.RGBA, error) {
	tmp, err := os.CreateTemp("", "remolo-grim-*.png")
	if err != nil {
		return nil, fmt.Errorf("desktop: grim tempfile: %w", err)
	}
	name := tmp.Name()
	tmp.Close()
	defer os.Remove(name)

	cmd := exec.Command("grim", name)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("desktop: grim: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return decodePNGFile(name, false)
}

// fileURIToPath converts a file:// URI (as returned by the portal) into a local
// filesystem path.
func fileURIToPath(uri string) (string, error) {
	if uri == "" {
		return "", errors.New("desktop: wayland portal: empty uri")
	}
	u, err := url.Parse(uri)
	if err != nil {
		return "", fmt.Errorf("desktop: wayland portal: parse uri: %w", err)
	}
	if u.Scheme != "file" {
		return "", fmt.Errorf("desktop: wayland portal: unexpected uri scheme %q", u.Scheme)
	}
	return u.Path, nil
}

// decodePNGFile reads and decodes a PNG file into an image.RGBA, optionally
// removing the file afterwards (portal screenshots land in a cache dir we should
// clean up).
func decodePNGFile(path string, remove bool) (*image.RGBA, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("desktop: open screenshot %q: %w", path, err)
	}
	src, err := png.Decode(f)
	f.Close()
	if remove {
		_ = os.Remove(path)
	}
	if err != nil {
		return nil, fmt.Errorf("desktop: decode screenshot png: %w", err)
	}
	if rgba, ok := src.(*image.RGBA); ok {
		return rgba, nil
	}
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Src)
	return dst, nil
}
