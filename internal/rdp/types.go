// Package rdp implements a client for Microsoft's Remote Desktop Protocol
// (MS-RDPBCGR and friends), from scratch. It targets a deliberately scoped
// MVP: TPKT/X.224 transport, TLS security only (no NLA/CredSSP — see the
// project plan for why that's a separate, much larger follow-up), MCS/GCC
// session setup, the standard capability/finalization sequence, Fast-Path
// bitmap updates (Interleaved RLE, the mandatory baseline codec) and
// Fast-Path input. No virtual channels, no GDI drawing orders, no
// RemoteFX/newer codecs.
package rdp

import "fmt"

// ProtocolError wraps a violation of the RDP protocol (malformed/unexpected
// data), as opposed to a plain I/O or TLS error.
type ProtocolError struct {
	Msg string
}

func (e *ProtocolError) Error() string { return "rdp: " + e.Msg }

func protoErrf(format string, args ...any) error {
	return &ProtocolError{Msg: fmt.Sprintf(format, args...)}
}

// UnsupportedError marks a case this client deliberately doesn't handle yet
// (e.g. a server that insists on NLA), so callers/UI can show a clear
// message instead of a confusing low-level parse failure.
type UnsupportedError struct {
	Msg string
}

func (e *UnsupportedError) Error() string { return "rdp: unsupported: " + e.Msg }

// X.224 / TPKT constants.
const (
	tpktVersion = 3

	x224TypeConnectionRequest = 0xE0
	x224TypeConnectionConfirm = 0xD0
	x224TypeData              = 0xF0
)

// RDP Negotiation Request/Response types and flags (part of the X.224
// Connection Request/Confirm PDUs).
const (
	negTypeRequest         = 0x01
	negTypeResponse        = 0x02
	negTypeFailure         = 0x03
	negProtocolSSL  uint32 = 0x00000001
)

// MCS (T.125) PDU choice tags: the high 6 bits of the first byte of every
// MCS Domain PDU, i.e. (choice << 2) | 2 low bits reserved for options that
// this client always leaves at 0. See mcs.go for the read/write helpers.
const (
	mcsChoiceErectDomainRequest = 1
	mcsChoiceAttachUserRequest  = 10
	mcsChoiceAttachUserConfirm  = 11
	mcsChoiceChannelJoinRequest = 14
	mcsChoiceChannelJoinConfirm = 15
	mcsChoiceSendDataRequest    = 25
	mcsChoiceSendDataIndication = 26
)

// mcsUserChannelBase is added to the MCS user ID assigned by Attach User
// Confirm to get that user's own dedicated MCS channel ID (MS-RDPBCGR
// 2.2.1.3.2 / T.125: user channel = base 1001 + UserId).
const mcsUserChannelBase = 1001

// The I/O channel ID itself is NOT a fixed constant — it's returned by the
// server in GCC Server Network Data (Connect Response) as MCSChannelId and
// stored on Client after parsing; see mcs.go/gcc.go.

// Fast-Path output header flags (server -> client).
const (
	fastPathOutputAction = 0x0 // low 2 bits of the first header byte == 0 => fast-path output
)

// Fast-Path update codes (within a Fast-Path Update PDU).
const (
	fastPathUpdateOrders      = 0x0
	fastPathUpdateBitmap      = 0x1
	fastPathUpdatePalette     = 0x2
	fastPathUpdateSynchronize = 0x3
	fastPathUpdatePointer     = 0x4 // covers the various pointer sub-types via a nested message type
)

// Bitmap Update rectangle flags.
const (
	bitmapCompression = 0x0001
)

// DialOptions configures an RDP session.
type DialOptions struct {
	Username string
	Password string
	Domain   string // optional
	// DialTimeout bounds the initial TCP connect. Zero means no timeout.
	DialTimeoutMS int
	// VerifyCertificate, if set, is called once the TLS handshake
	// completes with the hex-encoded SHA-256 fingerprint of the server's
	// leaf certificate. Returning an error aborts Dial before any
	// RDP-level data is sent. Left nil, the certificate is accepted
	// unconditionally (equivalent to v0.2.0's behavior) — callers should
	// always set this in production; it's optional here only so tests
	// and other callers that don't have a trust store handy still work.
	VerifyCertificate func(fingerprintHex string) error
}
