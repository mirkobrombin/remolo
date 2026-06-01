//go:build linux

package desktop

import (
	"fmt"
	"image"
	"os"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

// x11Capturer grabs the root window of the X server pointed to by $DISPLAY using
// a pure-Go XCB client (github.com/jezek/xgb). No cgo: the X11 protocol is spoken
// directly over the display socket.
//
// Capture issues an xproto.GetImage in ZPixmap format over the whole root
// window, then converts the server's pixel layout into image.RGBA. X servers on
// the common little-endian, 24/32-bit-depth TrueColor visuals deliver pixels as
// BGRX (blue, green, red, unused) bytes; we reorder them to RGBA.
type x11Capturer struct {
	conn *xgb.Conn
	root xproto.Window
	w, h int
	bgr  bool // server byte order implies BGRX (the usual little-endian case)
	buf  *image.RGBA
}

// newX11Capturer connects to the X server and reads the root geometry. If
// $DISPLAY is unset or the connection/handshake fails, it returns
// ErrCaptureUnavailable so the host can fall back to synthetic capture.
func newX11Capturer() (Capturer, error) {
	if os.Getenv("DISPLAY") == "" {
		return nil, ErrCaptureUnavailable
	}
	conn, err := xgb.NewConn()
	if err != nil {
		return nil, fmt.Errorf("%w: x11 connect: %v", ErrCaptureUnavailable, err)
	}
	setup := xproto.Setup(conn)
	if setup == nil || len(setup.Roots) == 0 {
		conn.Close()
		return nil, fmt.Errorf("%w: x11 setup had no screens", ErrCaptureUnavailable)
	}
	screen := setup.Roots[0]

	// Query the live root geometry rather than trusting the screen record, so a
	// resized or rotated display is captured at its current size.
	geomCookie := xproto.GetGeometry(conn, xproto.Drawable(screen.Root))
	geom, err := geomCookie.Reply()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("%w: x11 get geometry: %v", ErrCaptureUnavailable, err)
	}

	// ImageByteOrder: 0 = LSBFirst (little-endian -> BGRX), 1 = MSBFirst.
	bgr := setup.ImageByteOrder == 0

	c := &x11Capturer{
		conn: conn,
		root: screen.Root,
		w:    int(geom.Width),
		h:    int(geom.Height),
		bgr:  bgr,
		buf:  image.NewRGBA(image.Rect(0, 0, int(geom.Width), int(geom.Height))),
	}
	return c, nil
}

func (c *x11Capturer) Bounds() (int, int) { return c.w, c.h }

func (c *x11Capturer) Capture() (*image.RGBA, error) {
	// GetImage payloads can exceed the maximum request reply length when grabbing
	// a whole large screen, so request the image in horizontal bands sized to stay
	// within the server's reply budget.
	const planeMask = 0xffffffff
	maxRows := c.maxRowsPerRequest()
	if maxRows <= 0 {
		maxRows = 1
	}

	for y0 := 0; y0 < c.h; y0 += maxRows {
		rows := maxRows
		if y0+rows > c.h {
			rows = c.h - y0
		}
		cookie := xproto.GetImage(
			c.conn,
			xproto.ImageFormatZPixmap,
			xproto.Drawable(c.root),
			0, int16(y0),
			uint16(c.w), uint16(rows),
			planeMask,
		)
		reply, err := cookie.Reply()
		if err != nil {
			return nil, fmt.Errorf("desktop: x11 get image: %w", err)
		}
		c.convertBand(reply.Data, y0, rows)
	}
	return c.buf, nil
}

// maxRowsPerRequest computes how many full screen rows fit under the X protocol
// reply-length ceiling. The reply length is expressed in 4-byte units in a
// uint32, but practical servers cap a single GetImage to a few MiB; we use a
// conservative 8 MiB band budget at 4 bytes per pixel.
func (c *x11Capturer) maxRowsPerRequest() int {
	const bandBudget = 8 << 20 // 8 MiB
	rowBytes := c.w * 4
	if rowBytes <= 0 {
		return c.h
	}
	rows := bandBudget / rowBytes
	if rows < 1 {
		rows = 1
	}
	if rows > c.h {
		rows = c.h
	}
	return rows
}

// convertBand writes the ZPixmap bytes for a horizontal band starting at y0 into
// the RGBA buffer, reordering BGRX to RGBA when the server is little-endian.
func (c *x11Capturer) convertBand(data []byte, y0, rows int) {
	src := 0
	for y := 0; y < rows; y++ {
		dstRow := c.buf.PixOffset(0, y0+y)
		for x := 0; x < c.w; x++ {
			if src+4 > len(data) {
				return
			}
			b0 := data[src+0]
			b1 := data[src+1]
			b2 := data[src+2]
			d := dstRow + x*4
			if c.bgr {
				// BGRX -> RGBA
				c.buf.Pix[d+0] = b2
				c.buf.Pix[d+1] = b1
				c.buf.Pix[d+2] = b0
			} else {
				// XRGB (MSBFirst): bytes are X,R,G,B -> shift by one.
				c.buf.Pix[d+0] = b1
				c.buf.Pix[d+1] = b2
				if src+4 <= len(data) {
					c.buf.Pix[d+2] = data[src+3]
				}
			}
			c.buf.Pix[d+3] = 0xff
			src += 4
		}
	}
}

func (c *x11Capturer) Close() error {
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
	return nil
}
