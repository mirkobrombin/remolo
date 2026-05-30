package desktop

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	_ "image/png" // registered so older PNG-encoded frames still decode
)

// jpegQuality balances size and fidelity for desktop streaming. JPEG is far
// smaller and faster than PNG for full-screen frames, which is what keeps a live
// desktop smooth; the decoder sniffs the format, so this can change freely.
const jpegQuality = 85

func encodeImage(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func decodeImage(data []byte) (*image.RGBA, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if rgba, ok := img.(*image.RGBA); ok {
		return rgba, nil
	}
	// JPEG decodes to YCbCr; convert once to RGBA so the framebuffer copy uses
	// the fast path instead of a per-pixel At() loop.
	b := img.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Src)
	return dst, nil
}

// TileSize is the edge length, in pixels, of a diff tile.
const TileSize = 64

// frameKind tags an encoded frame payload so the decoder knows how to read it.
type frameKind byte

const (
	frameFull frameKind = 1 // a single full-image PNG
	frameDiff frameKind = 2 // zero or more changed tiles
)

// Wire format for an encoded frame (carried in one protocol FrameData payload):
//
//	[1 byte frameKind]
//	[4 byte big-endian uint32 frame width]
//	[4 byte big-endian uint32 frame height]
//	[4 byte big-endian uint32 sequence number]
//	... kind-specific body ...
//
// frameFull body:
//	[4 byte PNG length][PNG bytes]
//
// frameDiff body:
//	[4 byte big-endian uint32 tile count]
//	repeated tileCount times:
//	  [2 byte x][2 byte y][2 byte w][2 byte h][4 byte PNG length][PNG bytes]
//
// A frameDiff with zero tiles is a valid "nothing changed" frame.

const frameHeaderLen = 1 + 4 + 4 + 4

// Encoder turns successive RGBA frames into wire payloads for a chosen Mode. It
// is stateful: ModeDiff compares against the previously encoded frame. Encoder
// is not safe for concurrent use.
type Encoder struct {
	mode Mode
	seq  uint32
	prev *image.RGBA // last frame encoded, for diffing
}

// NewEncoder returns an Encoder for the given mode.
func NewEncoder(mode Mode) *Encoder {
	return &Encoder{mode: mode}
}

// Mode reports the encoder's current mode.
func (e *Encoder) Mode() Mode { return e.mode }

// SetMode switches modes at runtime. The diff baseline is reset so the next
// frame is sent in full.
func (e *Encoder) SetMode(mode Mode) {
	e.mode = mode
	e.prev = nil
}

// Encode produces the wire payload for one frame. For ModeDiff the first frame
// (or the first after a mode switch) is encoded as a full frame so the decoder
// has a complete baseline.
func (e *Encoder) Encode(img *image.RGBA) ([]byte, error) {
	e.seq++
	switch e.mode {
	case ModeScreenshot:
		return e.encodeFull(img)
	case ModeDiff:
		if e.prev == nil || !sameBounds(e.prev, img) {
			b, err := e.encodeFull(img)
			if err != nil {
				return nil, err
			}
			e.prev = cloneRGBA(img)
			return b, nil
		}
		b, err := e.encodeDiff(img)
		if err != nil {
			return nil, err
		}
		e.prev = cloneRGBA(img)
		return b, nil
	default:
		return nil, fmt.Errorf("desktop: unsupported encode mode %q", e.mode)
	}
}

func (e *Encoder) writeHeader(buf *bytes.Buffer, kind frameKind, w, h int) {
	buf.WriteByte(byte(kind))
	var b [12]byte
	binary.BigEndian.PutUint32(b[0:], uint32(w))
	binary.BigEndian.PutUint32(b[4:], uint32(h))
	binary.BigEndian.PutUint32(b[8:], e.seq)
	buf.Write(b[:])
}

func (e *Encoder) encodeFull(img *image.RGBA) ([]byte, error) {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	var buf bytes.Buffer
	e.writeHeader(&buf, frameFull, w, h)

	enc, err := encodeImage(img)
	if err != nil {
		return nil, fmt.Errorf("desktop: encode full frame: %w", err)
	}
	var lb [4]byte
	binary.BigEndian.PutUint32(lb[:], uint32(len(enc)))
	buf.Write(lb[:])
	buf.Write(enc)
	return buf.Bytes(), nil
}

func (e *Encoder) encodeDiff(img *image.RGBA) ([]byte, error) {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	var buf bytes.Buffer
	e.writeHeader(&buf, frameDiff, w, h)

	// Reserve space for the tile count and fill it in once known.
	countPos := buf.Len()
	buf.Write([]byte{0, 0, 0, 0})

	var tiles uint32
	for ty := 0; ty < h; ty += TileSize {
		th := minInt(TileSize, h-ty)
		for tx := 0; tx < w; tx += TileSize {
			tw := minInt(TileSize, w-tx)
			if tileEqual(e.prev, img, tx, ty, tw, th) {
				continue
			}
			sub := subImage(img, tx, ty, tw, th)
			enc, err := encodeImage(sub)
			if err != nil {
				return nil, fmt.Errorf("desktop: encode tile: %w", err)
			}
			var hdr [8]byte
			binary.BigEndian.PutUint16(hdr[0:], uint16(tx))
			binary.BigEndian.PutUint16(hdr[2:], uint16(ty))
			binary.BigEndian.PutUint16(hdr[4:], uint16(tw))
			binary.BigEndian.PutUint16(hdr[6:], uint16(th))
			buf.Write(hdr[:])
			var lb [4]byte
			binary.BigEndian.PutUint32(lb[:], uint32(len(enc)))
			buf.Write(lb[:])
			buf.Write(enc)
			tiles++
		}
	}
	b := buf.Bytes()
	binary.BigEndian.PutUint32(b[countPos:countPos+4], tiles)
	return b, nil
}

// Decoder reconstructs a framebuffer from successive wire payloads. It holds the
// running framebuffer so diff frames can be applied on top of it. Decoder is not
// safe for concurrent use.
type Decoder struct {
	fb *image.RGBA
}

// NewDecoder returns an empty Decoder.
func NewDecoder() *Decoder { return &Decoder{} }

// Decode applies one wire payload and returns the current full framebuffer. The
// returned image is the decoder's internal buffer; callers that retain it
// across Decode calls must copy it.
func (d *Decoder) Decode(payload []byte) (*image.RGBA, error) {
	if len(payload) < frameHeaderLen {
		return nil, fmt.Errorf("desktop: short frame (%d bytes)", len(payload))
	}
	kind := frameKind(payload[0])
	w := int(binary.BigEndian.Uint32(payload[1:5]))
	h := int(binary.BigEndian.Uint32(payload[5:9]))
	// payload[9:13] is the sequence number; not needed to reconstruct pixels.
	body := payload[frameHeaderLen:]

	switch kind {
	case frameFull:
		return d.decodeFull(w, h, body)
	case frameDiff:
		return d.decodeDiff(w, h, body)
	default:
		return nil, fmt.Errorf("desktop: unknown frame kind %d", kind)
	}
}

func (d *Decoder) decodeFull(w, h int, body []byte) (*image.RGBA, error) {
	if len(body) < 4 {
		return nil, fmt.Errorf("desktop: truncated full frame")
	}
	n := int(binary.BigEndian.Uint32(body[0:4]))
	body = body[4:]
	if len(body) < n {
		return nil, fmt.Errorf("desktop: full frame png truncated")
	}
	src, err := decodeImage(body[:n])
	if err != nil {
		return nil, fmt.Errorf("desktop: decode full frame: %w", err)
	}
	d.fb = image.NewRGBA(image.Rect(0, 0, w, h))
	drawInto(d.fb, src, 0, 0)
	return d.fb, nil
}

func (d *Decoder) decodeDiff(w, h int, body []byte) (*image.RGBA, error) {
	if d.fb == nil || d.fb.Bounds().Dx() != w || d.fb.Bounds().Dy() != h {
		// No usable baseline. Allocate a black frame so tiles can still apply.
		d.fb = image.NewRGBA(image.Rect(0, 0, w, h))
	}
	if len(body) < 4 {
		return nil, fmt.Errorf("desktop: truncated diff frame")
	}
	count := int(binary.BigEndian.Uint32(body[0:4]))
	body = body[4:]
	for i := 0; i < count; i++ {
		if len(body) < 12 {
			return nil, fmt.Errorf("desktop: truncated tile header")
		}
		tx := int(binary.BigEndian.Uint16(body[0:2]))
		ty := int(binary.BigEndian.Uint16(body[2:4]))
		// tile w/h at body[4:8] are implied by the decoded PNG bounds.
		n := int(binary.BigEndian.Uint32(body[8:12]))
		body = body[12:]
		if len(body) < n {
			return nil, fmt.Errorf("desktop: tile png truncated")
		}
		src, err := decodeImage(body[:n])
		if err != nil {
			return nil, fmt.Errorf("desktop: decode tile: %w", err)
		}
		drawInto(d.fb, src, tx, ty)
		body = body[n:]
	}
	return d.fb, nil
}

// --- helpers ---

func sameBounds(a, b *image.RGBA) bool {
	return a.Bounds().Dx() == b.Bounds().Dx() && a.Bounds().Dy() == b.Bounds().Dy()
}

func cloneRGBA(src *image.RGBA) *image.RGBA {
	dst := image.NewRGBA(src.Bounds())
	copy(dst.Pix, src.Pix)
	return dst
}

// tileEqual reports whether the tile at (tx,ty) of size tw x th is identical in
// prev and cur.
func tileEqual(prev, cur *image.RGBA, tx, ty, tw, th int) bool {
	for y := 0; y < th; y++ {
		po := prev.PixOffset(prev.Rect.Min.X+tx, prev.Rect.Min.Y+ty+y)
		co := cur.PixOffset(cur.Rect.Min.X+tx, cur.Rect.Min.Y+ty+y)
		if !bytes.Equal(prev.Pix[po:po+tw*4], cur.Pix[co:co+tw*4]) {
			return false
		}
	}
	return true
}

// subImage copies a w x h region at (x,y) of src into a fresh RGBA whose bounds
// start at (0,0), so PNG encoding yields a standalone tile image.
func subImage(src *image.RGBA, x, y, w, h int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for row := 0; row < h; row++ {
		so := src.PixOffset(src.Rect.Min.X+x, src.Rect.Min.Y+y+row)
		do := dst.PixOffset(0, row)
		copy(dst.Pix[do:do+w*4], src.Pix[so:so+w*4])
	}
	return dst
}

// drawInto copies src (any image) into dst at offset (x,y).
func drawInto(dst *image.RGBA, src image.Image, x, y int) {
	if rgba, ok := src.(*image.RGBA); ok {
		w, h := rgba.Bounds().Dx(), rgba.Bounds().Dy()
		for row := 0; row < h; row++ {
			so := rgba.PixOffset(rgba.Rect.Min.X, rgba.Rect.Min.Y+row)
			do := dst.PixOffset(x, y+row)
			copy(dst.Pix[do:do+w*4], rgba.Pix[so:so+w*4])
		}
		return
	}
	b := src.Bounds()
	for sy := b.Min.Y; sy < b.Max.Y; sy++ {
		for sx := b.Min.X; sx < b.Max.X; sx++ {
			dst.Set(x+(sx-b.Min.X), y+(sy-b.Min.Y), src.At(sx, sy))
		}
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
