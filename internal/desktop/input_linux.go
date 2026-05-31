//go:build linux

package desktop

import (
	"encoding/binary"
	"fmt"
	"os"
	"syscall"
)

// This file implements input injection through the Linux uinput subsystem in
// pure Go. It creates a virtual pointer + keyboard device under /dev/uinput,
// then writes input_event records to it. No cgo: ioctls go through
// syscall.Syscall(SYS_IOCTL, ...) and the device-setup struct is serialised by
// hand.
//
// References: linux/uinput.h and linux/input-event-codes.h.

// Event type codes (linux/input-event-codes.h).
const (
	evSyn = 0x00
	evKey = 0x01
	evRel = 0x02
	evAbs = 0x03
)

// Relative axis codes.
const (
	relX     = 0x00
	relY     = 0x01
	relWheel = 0x08
)

// Absolute axis codes.
const (
	absX = 0x00
	absY = 0x01
)

// SYN_REPORT, emitted to flush a batch of events.
const synReport = 0x00

// uinput ioctl request numbers (linux/uinput.h). These are _IOW/_IO encodings
// with the 'U' magic (0x55). They are fixed for the kernel ABI and identical
// across architectures.
const (
	uiSetEvBit   = 0x40045564 // _IOW(UINPUT_IOCTL_BASE, 100, int)
	uiSetKeyBit  = 0x40045565 // _IOW(UINPUT_IOCTL_BASE, 101, int)
	uiSetRelBit  = 0x40045566 // _IOW(UINPUT_IOCTL_BASE, 102, int)
	uiSetAbsBit  = 0x40045567 // _IOW(UINPUT_IOCTL_BASE, 103, int)
	uiDevCreate  = 0x5501     // _IO(UINPUT_IOCTL_BASE, 1)
	uiDevDestroy = 0x5502     // _IO(UINPUT_IOCTL_BASE, 2)
)

// uinputMaxNameSize matches UINPUT_MAX_NAME_SIZE in the kernel header.
const uinputMaxNameSize = 80

// absMax is the logical range of the absolute axes. Clients address the screen
// in pixels; we map 0..absMax onto the device, and callers move the pointer in
// the same coordinate space they capture in.
const absMax = 0x7fff

// uinputUserDev mirrors struct uinput_user_dev. It is written to the fd (legacy
// setup path) before UI_DEV_CREATE. Laid out for the kernel's C struct on
// 64-bit: name[80], input_id{bustype,vendor,product,version uint16}, ff_effects_max
// uint32, then absmax/absmin/absfuzz/absflat arrays of int32[ABS_CNT=64].
//
// We serialise it by hand with encoding/binary to avoid any unsafe layout
// assumptions about Go struct padding.
const absCnt = 64

func (d *uinputInjector) writeUserDev(name string) error {
	const size = uinputMaxNameSize + 8 + 4 + 4*absCnt*4
	buf := make([]byte, size)

	// name (null-padded).
	copy(buf[0:uinputMaxNameSize], name)

	off := uinputMaxNameSize
	// input_id: bustype, vendor, product, version (each uint16).
	binary.LittleEndian.PutUint16(buf[off+0:], 0x03) // BUS_USB
	binary.LittleEndian.PutUint16(buf[off+2:], 0x1234)
	binary.LittleEndian.PutUint16(buf[off+4:], 0x5678)
	binary.LittleEndian.PutUint16(buf[off+6:], 0x0001)
	off += 8

	// ff_effects_max uint32.
	binary.LittleEndian.PutUint32(buf[off:], 0)
	off += 4

	// absmax, absmin, absfuzz, absflat (int32[absCnt] each).
	absmaxOff := off
	absminOff := absmaxOff + absCnt*4
	// absX/absY max set to absMax, everything else left zero.
	binary.LittleEndian.PutUint32(buf[absmaxOff+absX*4:], uint32(absMax))
	binary.LittleEndian.PutUint32(buf[absmaxOff+absY*4:], uint32(absMax))
	binary.LittleEndian.PutUint32(buf[absminOff+absX*4:], 0)
	binary.LittleEndian.PutUint32(buf[absminOff+absY*4:], 0)

	if _, err := d.f.Write(buf); err != nil {
		return fmt.Errorf("desktop: write uinput_user_dev: %w", err)
	}
	return nil
}

// inputEvent mirrors struct input_event. On 64-bit Linux time is struct timeval
// {tv_sec int64; tv_usec int64}, followed by type uint16, code uint16, value
// int32. We serialise by hand (24 bytes payload, but the struct is 24 bytes on
// 64-bit due to natural alignment: 8+8+2+2+4 = 24).
const inputEventSize = 8 + 8 + 2 + 2 + 4

func encodeInputEvent(typ, code uint16, value int32) []byte {
	b := make([]byte, inputEventSize)
	// tv_sec, tv_usec left zero; the kernel fills timestamps.
	binary.LittleEndian.PutUint16(b[16:], typ)
	binary.LittleEndian.PutUint16(b[18:], code)
	binary.LittleEndian.PutUint32(b[20:], uint32(value))
	return b
}

type uinputInjector struct {
	f *os.File
}

// NewUinputInjector creates a virtual pointer + keyboard device via
// /dev/uinput. It requires write access to /dev/uinput (typically root or a
// udev rule); without it the call returns the underlying error (often EACCES),
// which the caller treats as "input injection unavailable".
func NewUinputInjector() (Injector, error) {
	f, err := os.OpenFile("/dev/uinput", os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("desktop: open /dev/uinput: %w", err)
	}
	d := &uinputInjector{f: f}

	if err := d.setup(); err != nil {
		f.Close()
		return nil, err
	}
	return d, nil
}

func (d *uinputInjector) setup() error {
	// Enable event types: keys (buttons + keyboard), absolute pointer, sync.
	if err := d.ioctl(uiSetEvBit, evKey); err != nil {
		return err
	}
	if err := d.ioctl(uiSetEvBit, evAbs); err != nil {
		return err
	}
	if err := d.ioctl(uiSetEvBit, evRel); err != nil {
		return err
	}
	if err := d.ioctl(uiSetEvBit, evSyn); err != nil {
		return err
	}

	// Absolute X/Y axes for pointer positioning.
	if err := d.ioctl(uiSetAbsBit, absX); err != nil {
		return err
	}
	if err := d.ioctl(uiSetAbsBit, absY); err != nil {
		return err
	}
	// Relative X/Y too, so relative motion works if a client uses it.
	if err := d.ioctl(uiSetRelBit, relX); err != nil {
		return err
	}
	if err := d.ioctl(uiSetRelBit, relWheel); err != nil {
		return err
	}
	if err := d.ioctl(uiSetRelBit, relY); err != nil {
		return err
	}

	// Enable the mouse buttons.
	for _, btn := range []int{BtnLeft, BtnRight, BtnMiddle} {
		if err := d.ioctl(uiSetKeyBit, btn); err != nil {
			return err
		}
	}
	// Enable a usable keyboard range (KEY_RESERVED..KEY_MAX is large; 0..255
	// covers the standard keys without enabling the full table).
	for kc := 1; kc <= 255; kc++ {
		if err := d.ioctl(uiSetKeyBit, kc); err != nil {
			return err
		}
	}

	if err := d.writeUserDev("remolo-virtual-input"); err != nil {
		return err
	}
	if err := d.ioctl(uiDevCreate, 0); err != nil {
		return fmt.Errorf("desktop: UI_DEV_CREATE: %w", err)
	}
	return nil
}

// ioctl issues an ioctl whose argument is a single integer passed by value
// (UI_SET_*BIT) or ignored (UI_DEV_CREATE/DESTROY).
func (d *uinputInjector) ioctl(req uintptr, arg int) error {
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL, d.f.Fd(), req, uintptr(arg))
	if errno != 0 {
		return fmt.Errorf("desktop: ioctl 0x%x: %w", req, errno)
	}
	return nil
}

func (d *uinputInjector) emit(typ, code uint16, value int32) error {
	if _, err := d.f.Write(encodeInputEvent(typ, code, value)); err != nil {
		return fmt.Errorf("desktop: emit event: %w", err)
	}
	return nil
}

func (d *uinputInjector) sync() error {
	return d.emit(evSyn, synReport, 0)
}

func (d *uinputInjector) MouseMove(x, y int) error {
	if err := d.emit(evAbs, absX, int32(x)); err != nil {
		return err
	}
	if err := d.emit(evAbs, absY, int32(y)); err != nil {
		return err
	}
	return d.sync()
}

func (d *uinputInjector) MouseButton(button int, down bool) error {
	v := int32(0)
	if down {
		v = 1
	}
	if err := d.emit(evKey, uint16(button), v); err != nil {
		return err
	}
	return d.sync()
}

func (d *uinputInjector) Key(keycode int, down bool) error {
	v := int32(0)
	if down {
		v = 1
	}
	if err := d.emit(evKey, uint16(keycode), v); err != nil {
		return err
	}
	return d.sync()
}

func (d *uinputInjector) Scroll(dy int) error {
	if err := d.emit(evRel, relWheel, int32(dy)); err != nil {
		return err
	}
	return d.sync()
}

func (d *uinputInjector) Close() error {
	// Best-effort destroy, then close the fd.
	_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, d.f.Fd(), uiDevDestroy, 0)
	return d.f.Close()
}
