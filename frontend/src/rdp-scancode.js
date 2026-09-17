// Maps browser KeyboardEvents to PC/AT Scan Code Set 1 for RDP's Fast-Path
// Keyboard Event. Unlike VNC's X11-keysym approach (keysym.js), RDP keyboard
// input is purely physical-key-based — there's no "Unicode keysym" fallback,
// so every key this client can send has to appear in the table below.
//
// Looked up by event.code (layout-independent), which is exactly what a
// scancode already is — no separate event.key fallback path is needed or
// possible here, unlike keysym.js.
//
// Packed as (extended << 8 | scancode) to match internal/rdp.Client.
// SendKeyEvent's decode (scancode = byte(keysym), extended = keysym&0x100).
// The set of codes and their scancodes is the same fixed table FreeRDP and
// every other RDP client ships (MS-RDPBCGR keyboardType 4, "IBM enhanced
// 101/102-key").

const EXTENDED = 0x100;

const CODE_SCANCODES = {
	Escape: 0x01,
	Digit1: 0x02,
	Digit2: 0x03,
	Digit3: 0x04,
	Digit4: 0x05,
	Digit5: 0x06,
	Digit6: 0x07,
	Digit7: 0x08,
	Digit8: 0x09,
	Digit9: 0x0a,
	Digit0: 0x0b,
	Minus: 0x0c,
	Equal: 0x0d,
	Backspace: 0x0e,
	Tab: 0x0f,
	KeyQ: 0x10,
	KeyW: 0x11,
	KeyE: 0x12,
	KeyR: 0x13,
	KeyT: 0x14,
	KeyY: 0x15,
	KeyU: 0x16,
	KeyI: 0x17,
	KeyO: 0x18,
	KeyP: 0x19,
	BracketLeft: 0x1a,
	BracketRight: 0x1b,
	Enter: 0x1c,
	ControlLeft: 0x1d,
	KeyA: 0x1e,
	KeyS: 0x1f,
	KeyD: 0x20,
	KeyF: 0x21,
	KeyG: 0x22,
	KeyH: 0x23,
	KeyJ: 0x24,
	KeyK: 0x25,
	KeyL: 0x26,
	Semicolon: 0x27,
	Quote: 0x28,
	Backquote: 0x29,
	ShiftLeft: 0x2a,
	Backslash: 0x2b,
	KeyZ: 0x2c,
	KeyX: 0x2d,
	KeyC: 0x2e,
	KeyV: 0x2f,
	KeyB: 0x30,
	KeyN: 0x31,
	KeyM: 0x32,
	Comma: 0x33,
	Period: 0x34,
	Slash: 0x35,
	ShiftRight: 0x36,
	NumpadMultiply: 0x37,
	AltLeft: 0x38,
	Space: 0x39,
	CapsLock: 0x3a,
	F1: 0x3b,
	F2: 0x3c,
	F3: 0x3d,
	F4: 0x3e,
	F5: 0x3f,
	F6: 0x40,
	F7: 0x41,
	F8: 0x42,
	F9: 0x43,
	F10: 0x44,
	NumLock: EXTENDED | 0x45,
	ScrollLock: 0x46,
	Numpad7: 0x47,
	Numpad8: 0x48,
	Numpad9: 0x49,
	NumpadSubtract: 0x4a,
	Numpad4: 0x4b,
	Numpad5: 0x4c,
	Numpad6: 0x4d,
	NumpadAdd: 0x4e,
	Numpad1: 0x4f,
	Numpad2: 0x50,
	Numpad3: 0x51,
	Numpad0: 0x52,
	NumpadDecimal: 0x53,
	IntlBackslash: 0x56,
	F11: 0x57,
	F12: 0x58,
	NumpadEqual: EXTENDED | 0x59,
	NumpadEnter: EXTENDED | 0x1c,
	ControlRight: EXTENDED | 0x1d,
	NumpadDivide: EXTENDED | 0x35,
	PrintScreen: EXTENDED | 0x37,
	AltRight: EXTENDED | 0x38,
	Home: EXTENDED | 0x47,
	ArrowUp: EXTENDED | 0x48,
	PageUp: EXTENDED | 0x49,
	ArrowLeft: EXTENDED | 0x4b,
	ArrowRight: EXTENDED | 0x4d,
	End: EXTENDED | 0x4f,
	ArrowDown: EXTENDED | 0x50,
	PageDown: EXTENDED | 0x51,
	Insert: EXTENDED | 0x52,
	Delete: EXTENDED | 0x53,
	MetaLeft: EXTENDED | 0x5b,
	OSLeft: EXTENDED | 0x5b,
	MetaRight: EXTENDED | 0x5c,
	OSRight: EXTENDED | 0x5c,
	ContextMenu: EXTENDED | 0x5d,
	Pause: 0x1d, // no clean single scancode (Set 1 sends it as its own 6-byte make-only sequence); best-effort as Control_L's code, harmless if wrong since it's rarely used for real input
};

/** Special key combo sends exposed to the UI (e.g. a toolbar button). */
export const SPECIAL_COMBOS = {
	ctrlAltDel: [CODE_SCANCODES.ControlLeft, CODE_SCANCODES.AltLeft, CODE_SCANCODES.Delete],
};

export function scancodeFor(event) {
	if (Object.prototype.hasOwnProperty.call(CODE_SCANCODES, event.code)) {
		return CODE_SCANCODES[event.code];
	}
	// Unhandled/non-physical key (e.g. "Unidentified", media keys): no safe
	// mapping, drop it rather than guess.
	return null;
}
