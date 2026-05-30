package desktop

import "image"

// syntheticCapturer generates a deterministic animated test pattern. Each
// Capture advances an internal frame counter, shifting a colour gradient so
// successive frames differ (exercising diff encoding) yet are fully
// reproducible. It backs the headless fallback and the codec tests.
type syntheticCapturer struct {
	w, h  int
	frame int
	buf   *image.RGBA
}

// NewSyntheticCapturer returns a Capturer that paints a moving gradient of size
// w x h. It never errors, so the capture -> codec -> stream -> decode pipeline
// keeps running on a machine with no reachable display.
func NewSyntheticCapturer(w, h int) Capturer {
	if w <= 0 {
		w = 1
	}
	if h <= 0 {
		h = 1
	}
	return &syntheticCapturer{
		w:   w,
		h:   h,
		buf: image.NewRGBA(image.Rect(0, 0, w, h)),
	}
}

func (s *syntheticCapturer) Bounds() (int, int) { return s.w, s.h }

func (s *syntheticCapturer) Capture() (*image.RGBA, error) {
	f := s.frame
	for y := 0; y < s.h; y++ {
		for x := 0; x < s.w; x++ {
			o := s.buf.PixOffset(x, y)
			s.buf.Pix[o+0] = byte((x + f) & 0xff)
			s.buf.Pix[o+1] = byte((y + f) & 0xff)
			s.buf.Pix[o+2] = byte((x + y + f) & 0xff)
			s.buf.Pix[o+3] = 0xff
		}
	}
	s.frame++
	return s.buf, nil
}

func (s *syntheticCapturer) Close() error { return nil }
