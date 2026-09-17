// Maps browser KeyboardEvents to X11 keysyms for the RFB KeyEvent message.
//
// Control/navigation/modifier/function/numpad keys are looked up by
// event.code (the physical key, layout-independent) so left/right
// modifiers and non-printable keys are always correct. Everything else
// falls back to event.key (which the browser has already resolved through
// the user's actual keyboard layout and modifier state) via the standard
// Latin-1-direct / "Unicode keysym" (0x01000000 + codepoint) convention
// used by X11/xkbcommon and by other VNC clients such as noVNC.
//
// Known limitation: dead keys and some AltGr/ISO-level-3 combinations on
// non-US layouts aren't specially handled — see the README roadmap.

const CODE_KEYSYMS = {
	Escape: 0xff1b,
	Tab: 0xff09,
	Enter: 0xff0d,
	NumpadEnter: 0xff8d,
	Backspace: 0xff08,
	Delete: 0xffff,
	Insert: 0xff63,
	Home: 0xff50,
	End: 0xff57,
	PageUp: 0xff55,
	PageDown: 0xff56,
	ArrowLeft: 0xff51,
	ArrowUp: 0xff52,
	ArrowRight: 0xff53,
	ArrowDown: 0xff54,
	ShiftLeft: 0xffe1,
	ShiftRight: 0xffe2,
	ControlLeft: 0xffe3,
	ControlRight: 0xffe4,
	AltLeft: 0xffe9,
	AltRight: 0xffea,
	MetaLeft: 0xffeb,
	MetaRight: 0xffec,
	OSLeft: 0xffeb,
	OSRight: 0xffec,
	ContextMenu: 0xff67,
	CapsLock: 0xffe5,
	NumLock: 0xff7f,
	ScrollLock: 0xff14,
	Pause: 0xff13,
	PrintScreen: 0xff61,
	Space: 0x0020,
	NumpadDecimal: 0xffae,
	NumpadAdd: 0xffab,
	NumpadSubtract: 0xffad,
	NumpadMultiply: 0xffaa,
	NumpadDivide: 0xffaf,
	NumpadEqual: 0xffbd,
};

for (let i = 1; i <= 24; i++) CODE_KEYSYMS[`F${i}`] = 0xffbe + (i - 1);
for (let i = 0; i <= 9; i++) CODE_KEYSYMS[`Numpad${i}`] = 0xffb0 + i;

/** Special key combo sends exposed to the UI (e.g. a toolbar button). */
export const SPECIAL_COMBOS = {
	ctrlAltDel: [0xffe3, 0xffe9, 0xffff], // Control_L, Alt_L, Delete
};

export function keysymFor(event) {
	if (Object.prototype.hasOwnProperty.call(CODE_KEYSYMS, event.code)) {
		return CODE_KEYSYMS[event.code];
	}

	const key = event.key;
	if (key && key.length === 1) {
		const cp = key.codePointAt(0);
		if ((cp >= 0x20 && cp <= 0x7e) || (cp >= 0xa0 && cp <= 0xff)) {
			return cp;
		}
		return 0x01000000 + cp;
	}

	// Unhandled named key (e.g. "Dead", "Unidentified", media keys): no
	// safe mapping, drop it rather than guess.
	return null;
}
