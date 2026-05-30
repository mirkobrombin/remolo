package desktop

// NopInjector discards all input events. It is the view-only Injector, used for
// sessions where input forwarding is disabled and for tests that must not touch
// /dev/uinput.
type NopInjector struct{}

func (NopInjector) MouseMove(x, y int) error                { return nil }
func (NopInjector) MouseButton(button int, down bool) error { return nil }
func (NopInjector) Key(keycode int, down bool) error        { return nil }
func (NopInjector) Scroll(dy int) error                     { return nil }
func (NopInjector) Close() error                            { return nil }
