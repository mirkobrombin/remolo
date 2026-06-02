package desktop

import (
	"image"
	"image/color"
	"net"
	"sync"
	"testing"
	"time"
)

// makePattern builds a deterministic RGBA image used by codec tests.
func makePattern(w, h, seed int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{
				R: byte((x + seed) & 0xff),
				G: byte((y + seed) & 0xff),
				B: byte((x*y + seed) & 0xff),
				A: 0xff,
			})
		}
	}
	return img
}

// pixelsEqual checks that two frames match within JPEG's lossy tolerance: same
// bounds and a small mean absolute per-channel error. The codec uses JPEG for
// size/speed, so exact equality is not expected.
func pixelsEqual(t *testing.T, want, got *image.RGBA) {
	t.Helper()
	if want.Bounds() != got.Bounds() {
		t.Fatalf("bounds differ: want %v got %v", want.Bounds(), got.Bounds())
	}
	if len(want.Pix) != len(got.Pix) {
		t.Fatalf("pixel buffer sizes differ: want %d got %d", len(want.Pix), len(got.Pix))
	}
	var sum int64
	for i := range want.Pix {
		d := int64(want.Pix[i]) - int64(got.Pix[i])
		if d < 0 {
			d = -d
		}
		sum += d
	}
	mean := float64(sum) / float64(len(want.Pix))
	// The synthetic test pattern is high-frequency noise (worst case for JPEG);
	// real screens compress far cleaner. This bound still catches structural
	// corruption (wrong offset/format), which yields a mean error in the 100s.
	if mean > 28 {
		t.Fatalf("mean absolute pixel error %.2f exceeds JPEG tolerance", mean)
	}
}

func TestScreenshotRoundTrip(t *testing.T) {
	enc := NewEncoder(ModeScreenshot)
	dec := NewDecoder()
	for seed := 0; seed < 3; seed++ {
		src := makePattern(200, 150, seed)
		payload, err := enc.Encode(src)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		got, err := dec.Decode(payload)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		pixelsEqual(t, src, got)
	}
}

func TestDiffRoundTrip(t *testing.T) {
	enc := NewEncoder(ModeDiff)
	dec := NewDecoder()

	// Frame 0: full baseline.
	f0 := makePattern(200, 150, 0)
	p0, err := enc.Encode(f0)
	if err != nil {
		t.Fatalf("encode f0: %v", err)
	}
	g0, err := dec.Decode(p0)
	if err != nil {
		t.Fatalf("decode f0: %v", err)
	}
	pixelsEqual(t, f0, g0)

	// Frame 1: identical, should produce a zero-tile diff but reconstruct equal.
	p1, err := enc.Encode(cloneRGBA(f0))
	if err != nil {
		t.Fatalf("encode f1: %v", err)
	}
	g1, err := dec.Decode(p1)
	if err != nil {
		t.Fatalf("decode f1: %v", err)
	}
	pixelsEqual(t, f0, g1)

	// Frame 2: mutate a single tile region; only that tile should change but the
	// whole reconstructed frame must match.
	f2 := cloneRGBA(f0)
	for y := 10; y < 40; y++ {
		for x := 10; x < 40; x++ {
			f2.Set(x, y, color.RGBA{R: 1, G: 2, B: 3, A: 255})
		}
	}
	p2, err := enc.Encode(f2)
	if err != nil {
		t.Fatalf("encode f2: %v", err)
	}
	g2, err := dec.Decode(p2)
	if err != nil {
		t.Fatalf("decode f2: %v", err)
	}
	pixelsEqual(t, f2, g2)
}

func TestSyntheticCapturerAnimates(t *testing.T) {
	c := NewSyntheticCapturer(80, 60)
	w, h := c.Bounds()
	if w != 80 || h != 60 {
		t.Fatalf("bounds: got %dx%d", w, h)
	}
	a, _ := c.Capture()
	first := cloneRGBA(a)
	b, _ := c.Capture()
	// Successive frames must differ so diff mode has something to send.
	same := true
	for i := range first.Pix {
		if first.Pix[i] != b.Pix[i] {
			same = false
			break
		}
	}
	if same {
		t.Fatal("synthetic frames did not change between captures")
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// runPipeline wires Serve(synthetic capturer) and Run over net.Pipe and counts
// decoded frames of the expected size.
func runPipeline(t *testing.T, mode Mode, wantFrames int) {
	t.Helper()
	srvConn, cliConn := net.Pipe()

	const w, h = 96, 64
	capr := NewSyntheticCapturer(w, h)

	var serveErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		serveErr = Serve(srvConn, capr, NopInjector{})
	}()

	frames := 0
	var lastW, lastH int
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = Run(cliConn, mode, 60, func(img *image.RGBA) {
			frames++
			lastW, lastH = img.Bounds().Dx(), img.Bounds().Dy()
			if frames >= wantFrames {
				cliConn.Close()
			}
		}, nil)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("pipeline timed out")
	}

	srvConn.Close()
	wg.Wait()

	if frames < wantFrames {
		t.Fatalf("got %d frames, want at least %d (serveErr=%v)", frames, wantFrames, serveErr)
	}
	if lastW != w || lastH != h {
		t.Fatalf("frame size %dx%d, want %dx%d", lastW, lastH, w, h)
	}
}

func TestPipelineScreenshot(t *testing.T) { runPipeline(t, ModeScreenshot, 5) }
func TestPipelineDiff(t *testing.T)       { runPipeline(t, ModeDiff, 5) }

func TestUinputInjector(t *testing.T) {
	inj, err := NewUinputInjector()
	if err != nil {
		t.Skipf("uinput unavailable (expected without permissions): %v", err)
	}
	defer inj.Close()
	if err := inj.MouseMove(100, 100); err != nil {
		t.Fatalf("mouse move: %v", err)
	}
}

// TestNewCapturerOrDegrades accepts either outcome: on a machine with a usable
// display NewCapturer returns a working capturer; on a headless box it returns
// ErrCaptureUnavailable so the host can fall back to synthetic capture. Either
// way the contract holds.
func TestNewCapturerOrDegrades(t *testing.T) {
	c, err := NewCapturer()
	if err != nil {
		t.Logf("NewCapturer unavailable (expected on headless): %v", err)
		return
	}
	defer c.Close()
	w, h := c.Bounds()
	if w <= 0 || h <= 0 {
		t.Fatalf("capturer reported non-positive bounds %dx%d", w, h)
	}
}
