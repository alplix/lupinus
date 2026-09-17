package rdp

import "bytes"

// Fast-Path input event codes (MS-RDPBCGR 2.2.8.1.2.2, eventHeader high 3
// bits).
const (
	fpInputEventScancode = 0
	fpInputEventMouse    = 1
)

// Keyboard event flags (low 5 bits of eventHeader for a scancode event).
const (
	kbdFlagsRelease  = 0x01
	kbdFlagsExtended = 0x02
)

// Pointer flags (MS-RDPBCGR 2.2.8.1.2.2.3, TS_FP_POINTER_EVENT).
const (
	ptrFlagsWheel     = 0x0200
	ptrFlagsWheelNeg  = 0x0100
	ptrFlagsHWheel    = 0x0400
	ptrFlagsMove      = 0x0800
	ptrFlagsDown      = 0x8000
	ptrFlagsButton1   = 0x1000 // left
	ptrFlagsButton2   = 0x2000 // right
	ptrFlagsButton3   = 0x4000 // middle
	wheelRotationMask = 0x01FF
)

// SendPointerEvent sends a Fast-Path Mouse Event. buttonMask matches the
// same bit layout wsbridge already uses for VNC (bit0=left, bit1=middle,
// bit2=right, bit3=wheel up, bit4=wheel down) — translated here to RDP's
// pointerFlags so viewer.js and wsbridge need no protocol-specific
// branching.
func (c *Client) SendPointerEvent(x, y int, buttonMask uint8) error {
	flags := uint16(ptrFlagsMove)
	if buttonMask&0x01 != 0 {
		flags |= ptrFlagsButton1 | ptrFlagsDown
	}
	if buttonMask&0x02 != 0 {
		flags |= ptrFlagsButton3 | ptrFlagsDown
	}
	if buttonMask&0x04 != 0 {
		flags |= ptrFlagsButton2 | ptrFlagsDown
	}

	var body bytes.Buffer
	if buttonMask&0x18 != 0 { // wheel up/down: send as its own event, no move/button bits
		wheelFlags := uint16(ptrFlagsWheel)
		if buttonMask&0x10 != 0 { // down = negative rotation
			wheelFlags |= ptrFlagsWheelNeg | (0x0078 & wheelRotationMask)
		} else {
			wheelFlags |= 0x0078 & wheelRotationMask
		}
		writeUint16LE(&body, wheelFlags)
	} else {
		writeUint16LE(&body, flags)
	}
	writeUint16LE(&body, uint16(x))
	writeUint16LE(&body, uint16(y))

	return c.sendFastPathInputEvent(fpInputEventMouse, 0, body.Bytes())
}

// SendKeyEvent sends a Fast-Path Keyboard Event. Unlike rfb.Client (X11
// keysyms), this client's "keysym" parameter is a PC/AT scancode packed
// as (extended<<8 | scancode) — see frontend/src/rdp-scancode.js, which
// performs the browser-KeyboardEvent-to-scancode mapping client-side, the
// same division of responsibility as the VNC keysym table.
func (c *Client) SendKeyEvent(keysym uint32, down bool) error {
	scancode := byte(keysym)
	extended := keysym&0x100 != 0

	eventFlags := 0
	if !down {
		eventFlags |= kbdFlagsRelease
	}
	if extended {
		eventFlags |= kbdFlagsExtended
	}

	return c.sendFastPathInputEvent(fpInputEventScancode, eventFlags, []byte{scancode})
}

// SendClientCutText is a no-op: the clipboard virtual channel isn't
// implemented in v0.2.0 (see the project plan). wsbridge.Session's
// InputSink interface still requires the method so the same Session type
// works with both rfb.Client and rdp.Client.
func (c *Client) SendClientCutText(text string) error { return nil }

// sendFastPathInputEvent wraps a single TS_FP_INPUT_EVENT in a Fast-Path
// Input PDU (MS-RDPBCGR 2.2.8.1.2) and sends it — one event per PDU, never
// batching multiple, which keeps this simple and is well within what
// every real server accepts.
func (c *Client) sendFastPathInputEvent(eventCode int, eventFlags int, data []byte) error {
	var event bytes.Buffer
	event.WriteByte(byte(eventCode<<5 | (eventFlags & 0x1F)))
	event.Write(data)
	payload := event.Bytes()

	// The length field's own size (1 or 2 bytes) affects the total it
	// needs to describe, so pick the form directly rather than going
	// through perWriteLength on a pre-computed guess.
	var pdu bytes.Buffer
	pdu.WriteByte(byte(1 << 2)) // fpInputHeader: action=0 (FASTPATH), numEvents=1, secFlags=0
	if shortTotal := 2 + len(payload); shortTotal <= 0x7F {
		pdu.WriteByte(byte(shortTotal))
	} else {
		v := uint16(3+len(payload)) | 0x8000
		pdu.WriteByte(byte(v >> 8))
		pdu.WriteByte(byte(v))
	}
	pdu.Write(payload)

	_, err := c.tc.Write(pdu.Bytes())
	return err
}
