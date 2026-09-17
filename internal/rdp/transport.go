package rdp

import (
	"bytes"
	"fmt"
	"io"
	"os"
)

func init() { debugWire = os.Getenv("RDP_DEBUG_WIRE") != "" }

// writeTPKT wraps an X.224/MCS payload in a 4-byte TPKT header (ITU-T
// T.123): version(1)=3, reserved(1)=0, total-length(2 big-endian, including
// this header). Every PDU on the wire — from the very first Connection
// Request through to the end of the MCS setup phase — is TPKT-framed;
// Fast-Path traffic (the bulk of a live session) is not, see fastpath.go.
func writeTPKT(w io.Writer, payload []byte) error {
	if len(payload)+4 > 0xFFFF {
		return protoErrf("TPKT payload too large (%d bytes)", len(payload))
	}
	buf := make([]byte, 4+len(payload))
	buf[0] = tpktVersion
	buf[1] = 0
	buf[2] = byte((len(payload) + 4) >> 8)
	buf[3] = byte(len(payload) + 4)
	copy(buf[4:], payload)
	if debugWire {
		fmt.Fprintf(osStderr, "DEBUG wire out (%d bytes): % x\n", len(buf), buf)
	}
	_, err := w.Write(buf)
	return err
}

var debugWire = false

// readTPKT reads one TPKT-framed PDU and returns its payload (everything
// after the 4-byte header).
func readTPKT(r io.Reader) ([]byte, error) {
	hdr, err := readFull(r, 4)
	if err != nil {
		return nil, err
	}
	if hdr[0] != tpktVersion {
		return nil, protoErrf("unexpected TPKT version %d", hdr[0])
	}
	total := int(hdr[2])<<8 | int(hdr[3])
	if total < 4 {
		return nil, protoErrf("TPKT length %d smaller than header", total)
	}
	return readFull(r, total-4)
}

// handleSlowPathPDU reads one TPKT-framed PDU whose version byte (0x03)
// has already been consumed by the caller. Even with Fast-Path output
// negotiated, this server interleaves genuine slow-path Data PDUs into
// the graphics stream — confirmed by capturing and hand-decoding real
// traffic: pduType2 PDUTYPE2_UPDATE(2) carrying a TS_UPDATE_DATA whose
// updateType is UPDATETYPE_BITMAP(1), wrapping the exact same
// TS_UPDATE_BITMAP_DATA structure Fast-Path bitmap updates use (see
// bitmap.go's decodeBitmapUpdate). Anything else slow-path is consumed
// and ignored — this client doesn't act on orders/palette/etc — but must
// still be parsed enough to stay byte-aligned for the next PDU.
func (c *Client) handleSlowPathPDU(sink FramebufferSink) error {
	rest, err := readFull(c.tc, 3) // reserved(1) + length(2 BE)
	if err != nil {
		return err
	}
	total := int(rest[1])<<8 | int(rest[2])
	if total < 4 {
		return protoErrf("slow-path PDU during Fast-Path stream: length %d smaller than header", total)
	}
	body, err := readFull(c.tc, total-4)
	if err != nil {
		return err
	}
	if debugWire {
		fmt.Fprintf(osStderr, "DEBUG handleSlowPathPDU: total=%d body=% x\n", total, body)
	}

	// body: X.224 Data header(3) + MCS SendDataIndication envelope
	// (choice(1)+initiator(2)+channelId(2)+priority(1)+PER length(1-2)).
	if len(body) < 3 || body[1] != x224TypeData {
		return protoErrf("slow-path PDU: expected X.224 Data TPDU")
	}
	mcs := body[3:]
	if len(mcs) < 6 {
		return protoErrf("slow-path PDU: MCS envelope too short")
	}
	if choice := mcs[0] >> 2; int(choice) != mcsChoiceSendDataIndication {
		return protoErrf("slow-path PDU: expected SendDataIndication (choice %d), got %d", mcsChoiceSendDataIndication, choice)
	}
	channelID := uint16(mcs[3])<<8 | uint16(mcs[4])
	rest2 := mcs[6:] // past choice, initiator(2), channelId(2), priority(1)
	length, n, err := readPERLength(rest2)
	if err != nil {
		return err
	}
	data := rest2[n:]
	if length > len(data) {
		return protoErrf("slow-path PDU: declared length %d exceeds remaining %d bytes", length, len(data))
	}
	data = data[:length]

	if channelID != c.ioChannelID {
		if channelID == c.cliprdrChannelID {
			return c.handleVirtualChannelChunk(data, sink)
		}
		return nil // some other channel this client never joined: ignore
	}

	// ShareControlHeader(6) + ShareDataHeader(12).
	if len(data) < 6+12 {
		return protoErrf("slow-path PDU: share headers too short (%d bytes)", len(data))
	}
	pduType2 := data[6+8]
	payload := data[6+12:]

	const pduType2Update = 2
	const updateTypeBitmap = 1
	if pduType2 != pduType2Update {
		return nil // orders/palette/control/etc: not implemented, safe to ignore
	}
	if len(payload) < 2 {
		return protoErrf("slow-path update PDU too short")
	}
	updateType := uint16(payload[0]) | uint16(payload[1])<<8
	if updateType != updateTypeBitmap {
		return nil
	}
	return decodeBitmapUpdate(payload[2:], sink)
}

// x224DataHeader is the fixed 3-byte X.224 Data (DT) TPDU header: length
// indicator (2, i.e. the two bytes that follow), code 0xF0, then the
// EOT/ROA octet (0x80 — end of TSDU, the only value RDP ever uses). Every
// PDU after the initial Connection Request/Confirm — MCS Connect-Initial
// onward, for the whole lifetime of the (non-Fast-Path) connection — is
// wrapped TPKT(X.224 Data(payload)), not just TPKT(payload).
var x224DataHeader = []byte{0x02, 0xF0, 0x80}

func writeX224Data(w io.Writer, payload []byte) error {
	return writeTPKT(w, append(append([]byte{}, x224DataHeader...), payload...))
}

func readX224Data(r io.Reader) ([]byte, error) {
	payload, err := readTPKT(r)
	if err != nil {
		return nil, err
	}
	if len(payload) < 3 || payload[1] != x224TypeData {
		return nil, protoErrf("expected X.224 Data TPDU, got % x", payload)
	}
	return payload[3:], nil
}

// buildConnectionRequest builds the X.224 Connection Request TPDU
// (MS-RDPBCGR 2.2.1.1), including an informational routing cookie and an
// RDP Negotiation Request advertising PROTOCOL_SSL only — this client
// never offers RDP Standard Security (legacy RC4) or Hybrid/NLA (see the
// project plan for why NLA is a deliberate, documented gap).
func buildConnectionRequest(username string) []byte {
	var cookie []byte
	if username != "" {
		cookie = []byte(fmt.Sprintf("Cookie: mstshash=%s\r\n", username))
	}

	negReq := make([]byte, 8)
	negReq[0] = negTypeRequest
	negReq[1] = 0 // flags
	negReq[2], negReq[3] = 8, 0
	negReq[4] = byte(negProtocolSSL)
	negReq[5] = byte(negProtocolSSL >> 8)
	negReq[6] = byte(negProtocolSSL >> 16)
	negReq[7] = byte(negProtocolSSL >> 24)

	variable := append(cookie, negReq...)

	header := []byte{
		byte(6 + len(variable)), // length indicator: everything below except itself
		x224TypeConnectionRequest,
		0, 0, // dst-ref
		0, 0, // src-ref
		0, // class option
	}
	return append(header, variable...)
}

// parseConnectionConfirm reads the server's X.224 Connection Confirm and
// its RDP Negotiation Response, returning the negotiated security
// protocol (always negProtocolSSL on success — anything else is either a
// hard failure or a protocol this client doesn't implement).
func parseConnectionConfirm(payload []byte) (uint32, error) {
	if len(payload) < 7 {
		return 0, protoErrf("connection confirm too short (%d bytes)", len(payload))
	}
	if payload[1] != x224TypeConnectionConfirm {
		return 0, protoErrf("expected X.224 connection confirm, got type 0x%02x", payload[1])
	}

	rest := payload[7:] // past li, code, dst-ref, src-ref, class
	if len(rest) == 0 {
		return 0, &UnsupportedError{Msg: "server sent no RDP Negotiation Response — likely only supports legacy RDP Standard Security, not implemented"}
	}

	r := bytes.NewReader(rest)
	negType, err := readUint8(r)
	if err != nil {
		return 0, err
	}
	if _, err := readUint8(r); err != nil { // flags
		return 0, err
	}
	length, err := readUint16LE(r)
	if err != nil {
		return 0, err
	}

	switch negType {
	case negTypeResponse:
		if length != 8 {
			return 0, protoErrf("unexpected negotiation response length %d", length)
		}
		selected, err := readUint32LE(r)
		if err != nil {
			return 0, err
		}
		if selected != negProtocolSSL {
			return 0, &UnsupportedError{Msg: fmt.Sprintf("server selected security protocol 0x%x, only TLS (PROTOCOL_SSL) is implemented", selected)}
		}
		return selected, nil
	case negTypeFailure:
		code, err := readUint32LE(r)
		if err != nil {
			return 0, err
		}
		return 0, &UnsupportedError{Msg: fmt.Sprintf("server refused negotiation (failure code 0x%x) — if this is a Windows host, it likely requires NLA, which isn't implemented yet", code)}
	default:
		return 0, protoErrf("unexpected negotiation message type 0x%02x", negType)
	}
}
