//go:build windows

package desktop

import (
	"errors"
	"fmt"
	"image"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ErrCaptureUnavailable is returned by NewCapturer when no real screen-capture
// backend can be initialised. On Windows this is unlikely, but the host treats
// it as the signal to fall back to NewSyntheticCapturer.
var ErrCaptureUnavailable = errors.New(
	"desktop: screen capture not available on this system")

// This file implements primary-display capture on Windows in pure Go (no cgo)
// using the GDI APIs from gdi32.dll and user32.dll. For each frame it BitBlts
// the screen DC into an in-memory DIB and reads the pixels back with GetDIBits.
// GDI returns 32-bit DIB pixels as BGRA, which we reorder to RGBA.

var (
	modUser32 = windows.NewLazySystemDLL("user32.dll")
	modGDI32  = windows.NewLazySystemDLL("gdi32.dll")

	procGetDC               = modUser32.NewProc("GetDC")
	procReleaseDC           = modUser32.NewProc("ReleaseDC")
	procGetSystemMetrics    = modUser32.NewProc("GetSystemMetrics")
	procCreateCompatibleDC  = modGDI32.NewProc("CreateCompatibleDC")
	procCreateCompatibleBmp = modGDI32.NewProc("CreateCompatibleBitmap")
	procSelectObject        = modGDI32.NewProc("SelectObject")
	procBitBlt              = modGDI32.NewProc("BitBlt")
	procGetDIBits           = modGDI32.NewProc("GetDIBits")
	procDeleteObject        = modGDI32.NewProc("DeleteObject")
	procDeleteDC            = modGDI32.NewProc("DeleteDC")
)

// GetSystemMetrics indices.
const (
	smCXScreen = 0
	smCYScreen = 1
)

// BitBlt raster operation: plain source copy.
const srcCopy = 0x00CC0020

// DIB constants.
const (
	biRGB        = 0
	dibRGBColors = 0
)

// bitmapInfoHeader mirrors the Win32 BITMAPINFOHEADER struct.
type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

// bitmapInfo is BITMAPINFOHEADER plus a one-entry color table placeholder.
type bitmapInfo struct {
	Header bitmapInfoHeader
	Colors [1]uint32
}

// gdiCapturer holds the reusable device contexts and bitmap so successive
// captures avoid re-allocating GDI objects.
type gdiCapturer struct {
	w, h     int
	screenDC uintptr
	memDC    uintptr
	bitmap   uintptr
	oldObj   uintptr
	buf      *image.RGBA
	raw      []byte
}

// NewCapturer returns a GDI-backed capturer for the primary display.
func NewCapturer() (Capturer, error) {
	w := getSystemMetrics(smCXScreen)
	h := getSystemMetrics(smCYScreen)
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("%w: bad screen metrics %dx%d", ErrCaptureUnavailable, w, h)
	}

	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return nil, fmt.Errorf("%w: GetDC failed", ErrCaptureUnavailable)
	}

	memDC, _, _ := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		procReleaseDC.Call(0, screenDC)
		return nil, fmt.Errorf("%w: CreateCompatibleDC failed", ErrCaptureUnavailable)
	}

	bitmap, _, _ := procCreateCompatibleBmp.Call(screenDC, uintptr(w), uintptr(h))
	if bitmap == 0 {
		procDeleteDC.Call(memDC)
		procReleaseDC.Call(0, screenDC)
		return nil, fmt.Errorf("%w: CreateCompatibleBitmap failed", ErrCaptureUnavailable)
	}

	oldObj, _, _ := procSelectObject.Call(memDC, bitmap)

	return &gdiCapturer{
		w:        w,
		h:        h,
		screenDC: screenDC,
		memDC:    memDC,
		bitmap:   bitmap,
		oldObj:   oldObj,
		buf:      image.NewRGBA(image.Rect(0, 0, w, h)),
		raw:      make([]byte, w*h*4),
	}, nil
}

func getSystemMetrics(index int) int {
	r, _, _ := procGetSystemMetrics.Call(uintptr(index))
	return int(int32(r))
}

func (c *gdiCapturer) Bounds() (int, int) { return c.w, c.h }

func (c *gdiCapturer) Capture() (*image.RGBA, error) {
	// Copy the screen into the memory bitmap.
	ret, _, _ := procBitBlt.Call(
		c.memDC, 0, 0, uintptr(c.w), uintptr(c.h),
		c.screenDC, 0, 0, srcCopy,
	)
	if ret == 0 {
		return nil, errors.New("desktop: BitBlt failed")
	}

	// Request a top-down 32-bit DIB by giving a negative height.
	bi := bitmapInfo{
		Header: bitmapInfoHeader{
			Size:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
			Width:       int32(c.w),
			Height:      -int32(c.h), // negative -> top-down rows
			Planes:      1,
			BitCount:    32,
			Compression: biRGB,
		},
	}

	ret, _, _ = procGetDIBits.Call(
		c.memDC,
		c.bitmap,
		0,
		uintptr(c.h),
		uintptr(unsafe.Pointer(&c.raw[0])),
		uintptr(unsafe.Pointer(&bi)),
		dibRGBColors,
	)
	if ret == 0 {
		return nil, errors.New("desktop: GetDIBits failed")
	}

	// Convert BGRA -> RGBA.
	for i := 0; i+3 < len(c.raw); i += 4 {
		c.buf.Pix[i+0] = c.raw[i+2]
		c.buf.Pix[i+1] = c.raw[i+1]
		c.buf.Pix[i+2] = c.raw[i+0]
		c.buf.Pix[i+3] = 0xff
	}
	return c.buf, nil
}

func (c *gdiCapturer) Close() error {
	if c.memDC != 0 && c.oldObj != 0 {
		procSelectObject.Call(c.memDC, c.oldObj)
	}
	if c.bitmap != 0 {
		procDeleteObject.Call(c.bitmap)
		c.bitmap = 0
	}
	if c.memDC != 0 {
		procDeleteDC.Call(c.memDC)
		c.memDC = 0
	}
	if c.screenDC != 0 {
		procReleaseDC.Call(0, c.screenDC)
		c.screenDC = 0
	}
	return nil
}
