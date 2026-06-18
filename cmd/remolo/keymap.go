package main

// keycodeFor maps a DOM KeyboardEvent.code to a Linux input-event-codes KEY_*
// value, which is what the host's uinput injector expects.
func keycodeFor(code string) (int, bool) {
	kc, ok := jsToLinuxKey[code]
	return kc, ok
}

// jsToLinuxKey covers the keys a remote desktop session realistically needs.
// Values are from linux/input-event-codes.h.
var jsToLinuxKey = map[string]int{
	// Letters.
	"KeyA": 30, "KeyB": 48, "KeyC": 46, "KeyD": 32, "KeyE": 18, "KeyF": 33,
	"KeyG": 34, "KeyH": 35, "KeyI": 23, "KeyJ": 36, "KeyK": 37, "KeyL": 38,
	"KeyM": 50, "KeyN": 49, "KeyO": 24, "KeyP": 25, "KeyQ": 16, "KeyR": 19,
	"KeyS": 31, "KeyT": 20, "KeyU": 22, "KeyV": 47, "KeyW": 17, "KeyX": 45,
	"KeyY": 21, "KeyZ": 44,

	// Digits row.
	"Digit1": 2, "Digit2": 3, "Digit3": 4, "Digit4": 5, "Digit5": 6,
	"Digit6": 7, "Digit7": 8, "Digit8": 9, "Digit9": 10, "Digit0": 11,

	// Editing / whitespace.
	"Enter": 28, "Escape": 1, "Backspace": 14, "Tab": 15, "Space": 57,
	"CapsLock": 58, "Delete": 111, "Insert": 110,

	// Punctuation.
	"Minus": 12, "Equal": 13, "BracketLeft": 26, "BracketRight": 27,
	"Backslash": 43, "Semicolon": 39, "Quote": 40, "Backquote": 41,
	"Comma": 51, "Period": 52, "Slash": 53,

	// Modifiers.
	"ShiftLeft": 42, "ShiftRight": 54, "ControlLeft": 29, "ControlRight": 97,
	"AltLeft": 56, "AltRight": 100, "MetaLeft": 125, "MetaRight": 126,

	// Navigation.
	"ArrowUp": 103, "ArrowDown": 108, "ArrowLeft": 105, "ArrowRight": 106,
	"Home": 102, "End": 107, "PageUp": 104, "PageDown": 109,

	// Function keys.
	"F1": 59, "F2": 60, "F3": 61, "F4": 62, "F5": 63, "F6": 64,
	"F7": 65, "F8": 66, "F9": 67, "F10": 68, "F11": 87, "F12": 88,

	// Numpad (common subset).
	"NumpadEnter": 96, "NumpadAdd": 78, "NumpadSubtract": 74,
	"NumpadMultiply": 55, "NumpadDivide": 98,
}
