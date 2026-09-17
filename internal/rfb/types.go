// Package rfb implements a client for the RFB (Remote Framebuffer) protocol,
// RFC 6143 — the protocol spoken by VNC servers. It is a from-scratch
// implementation covering the security types and encodings Lupinus v0.1.0
// targets: None/VNC-Authentication security, and Raw/CopyRect/ZRLE encodings
// plus the DesktopSize, ExtendedDesktopSize and Cursor pseudo-encodings.
package rfb

import "fmt"

// Security types (RFC 6143 §7.1.2).
const (
	secInvalid = 0
	secNone    = 1
	secVNCAuth = 2
)

// Client-to-server message types (RFC 6143 §7.5).
const (
	msgSetPixelFormat           = 0
	msgSetEncodings             = 2
	msgFramebufferUpdateRequest = 3
	msgKeyEvent                 = 4
	msgPointerEvent             = 5
	msgClientCutText            = 6
)

// Server-to-client message types (RFC 6143 §7.6).
const (
	msgFramebufferUpdate   = 0
	msgSetColourMapEntries = 1
	msgBell                = 2
	msgServerCutText       = 3
)

// Encoding types (RFC 6143 §7.7 and the pseudo-encoding extensions we use).
const (
	EncodingRaw                 = 0
	EncodingCopyRect            = 1
	EncodingTight               = 7
	EncodingZRLE                = 16
	EncodingCursor              = -239 // pseudo-encoding
	EncodingDesktopSize         = -223 // pseudo-encoding
	EncodingExtendedDesktopSize = -308 // pseudo-encoding
)

// PixelFormat mirrors the 16-byte PIXEL_FORMAT structure. Lupinus always
// requests the same fixed format from the server (see requestedPixelFormat
// in handshake.go) so the decoder never has to deal with arbitrary
// server-native depths/shifts.
type PixelFormat struct {
	BitsPerPixel uint8
	Depth        uint8
	BigEndian    uint8
	TrueColor    uint8
	RedMax       uint16
	GreenMax     uint16
	BlueMax      uint16
	RedShift     uint8
	GreenShift   uint8
	BlueShift    uint8
}

// requestedPixelFormat is the format Lupinus always asks the server to use:
// 32 bits per pixel, 24-bit colour, little-endian, true-colour, with R at
// byte 0 / G at byte 1 / B at byte 2 on the wire. That byte order is exactly
// RGB (plus an unused 4th byte we overwrite with alpha=255), so decoded Raw
// and ZRLE pixel data can be handed to an HTML canvas ImageData buffer with
// no channel shuffling.
var requestedPixelFormat = PixelFormat{
	BitsPerPixel: 32,
	Depth:        24,
	BigEndian:    0,
	TrueColor:    1,
	RedMax:       255,
	GreenMax:     255,
	BlueMax:      255,
	RedShift:     0,
	GreenShift:   8,
	BlueShift:    16,
}

// ProtocolError wraps a violation of the RFB protocol (as opposed to a plain
// I/O error), so callers can distinguish "server misbehaved" from "network
// dropped".
type ProtocolError struct {
	Msg string
}

func (e *ProtocolError) Error() string { return "rfb: " + e.Msg }

func protoErrf(format string, args ...any) error {
	return &ProtocolError{Msg: fmt.Sprintf(format, args...)}
}

// AuthError indicates the server rejected our credentials (or we have none
// to offer for a security type it required).
type AuthError struct {
	Reason string
}

func (e *AuthError) Error() string {
	if e.Reason == "" {
		return "rfb: authentication failed"
	}
	return "rfb: authentication failed: " + e.Reason
}
