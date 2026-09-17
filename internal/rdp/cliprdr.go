package rdp

import (
	"bytes"
	"unicode/utf16"
)

// Clipboard Virtual Channel Extension (MS-RDPECLIP) — a from-scratch
// implementation of the subset needed to sync plain text both directions,
// mirroring the CutText/SendClientCutText contract rfb.Client already has.
// Scope deliberately excludes file transfer (CB_FILECONTENTS_*) and the
// long-format-names capability: CF_UNICODETEXT is a well-known registered
// format, so short format names (fixed 32-byte, usually-empty name field)
// are sufficient and simpler to get byte-exact.

// Clipboard PDU msgType (MS-RDPECLIP 2.2.2.1, CLIPRDR_HEADER.msgType).
const (
	cbMonitorReady       = 0x0001
	cbFormatList         = 0x0002
	cbFormatListResponse = 0x0003
	cbFormatDataRequest  = 0x0004
	cbFormatDataResponse = 0x0005
	cbClipCaps           = 0x0007
)

const (
	cbResponseOK   = 0x0001
	cbResponseFail = 0x0002
)

const cbCapstypeGeneral = 0x0001
const cbCapsVersion1 = 0x00000001

// CF_UNICODETEXT — the standard Windows clipboard format ID for
// null-terminated UTF-16LE text (winuser.h). The only format this client
// offers or requests.
const cfUnicodeText = 13

// Virtual Channel PDU chunk flags (MS-RDPBCGR 2.2.6.1, CHANNEL_PDU_HEADER.flags).
const (
	channelFlagFirst = 0x00000001
	channelFlagLast  = 0x00000002
)

const virtualChannelChunkSize = 1600

// handleVirtualChannelChunk reassembles one CHANNEL_PDU_HEADER-framed
// chunk (length(4 LE) + flags(4 LE) + chunk data) and, once a complete
// message has arrived (CHANNEL_FLAG_LAST seen), dispatches it. The only
// virtual channel this client ever joins is cliprdr, so no channel-ID
// bookkeeping beyond that is needed here.
func (c *Client) handleVirtualChannelChunk(data []byte, sink FramebufferSink) error {
	if len(data) < 8 {
		return protoErrf("virtual channel PDU too short (%d bytes)", len(data))
	}
	flags := uint32(data[4]) | uint32(data[5])<<8 | uint32(data[6])<<16 | uint32(data[7])<<24
	chunk := data[8:]

	if flags&channelFlagFirst != 0 {
		c.vcBuf = c.vcBuf[:0]
	}
	c.vcBuf = append(c.vcBuf, chunk...)

	if flags&channelFlagLast == 0 {
		return nil // wait for the remaining fragments
	}
	buf := c.vcBuf
	c.vcBuf = nil
	return c.handleCliprdrPDU(buf, sink)
}

// sendVirtualChannelData wraps payload in one or more CHANNEL_PDU_HEADER
// chunks (splitting at virtualChannelChunkSize, matching every real
// client's chunking behavior even though this client's own clipboard PDUs
// rarely need more than one chunk) and sends each over channelID.
func (c *Client) sendVirtualChannelData(channelID uint16, payload []byte) error {
	total := len(payload)
	for offset := 0; offset == 0 || offset < total; {
		end := offset + virtualChannelChunkSize
		if end > total {
			end = total
		}
		var flags uint32
		if offset == 0 {
			flags |= channelFlagFirst
		}
		if end == total {
			flags |= channelFlagLast
		}

		var pdu bytes.Buffer
		writeUint32LE(&pdu, uint32(total))
		writeUint32LE(&pdu, flags)
		pdu.Write(payload[offset:end])
		if err := c.sendMCSData(channelID, pdu.Bytes()); err != nil {
			return err
		}
		offset = end
	}
	return nil
}

func cliprdrHeader(msgType, msgFlags uint16, dataLen int) []byte {
	var b bytes.Buffer
	writeUint16LE(&b, msgType)
	writeUint16LE(&b, msgFlags)
	writeUint32LE(&b, uint32(dataLen))
	return b.Bytes()
}

// sendClipCaps announces this client's (minimal) clipboard capabilities:
// version 1, no optional feature flags (no file transfer, no long format
// names, no locking).
func (c *Client) sendClipCaps() error {
	var caps bytes.Buffer
	writeUint16LE(&caps, cbCapstypeGeneral)
	writeUint16LE(&caps, 12) // lengthCapability: 4-byte set header + 8 bytes of data
	writeUint32LE(&caps, cbCapsVersion1)
	writeUint32LE(&caps, 0) // generalFlags: none

	var body bytes.Buffer
	writeUint16LE(&body, 1) // cCapabilitiesSets
	writeUint16LE(&body, 0) // pad1
	body.Write(caps.Bytes())

	pdu := append(cliprdrHeader(cbClipCaps, 0, body.Len()), body.Bytes()...)
	return c.sendVirtualChannelData(c.cliprdrChannelID, pdu)
}

// sendFormatList announces which formats this client currently has to
// offer. An empty list is a valid "nothing to offer yet" announcement,
// sent once at startup; a non-empty CF_UNICODETEXT entry is sent whenever
// SendClientCutText hands this client new text to share.
func (c *Client) sendFormatList(haveText bool) error {
	var body bytes.Buffer
	if haveText {
		writeUint32LE(&body, cfUnicodeText)
		body.Write(make([]byte, 32)) // formatName: empty for a well-known format
	}
	pdu := append(cliprdrHeader(cbFormatList, 0, body.Len()), body.Bytes()...)
	return c.sendVirtualChannelData(c.cliprdrChannelID, pdu)
}

func (c *Client) sendFormatListResponse(ok bool) error {
	flags := uint16(cbResponseFail)
	if ok {
		flags = cbResponseOK
	}
	pdu := cliprdrHeader(cbFormatListResponse, flags, 0)
	return c.sendVirtualChannelData(c.cliprdrChannelID, pdu)
}

func (c *Client) sendFormatDataRequest(formatID uint32) error {
	var body bytes.Buffer
	writeUint32LE(&body, formatID)
	pdu := append(cliprdrHeader(cbFormatDataRequest, 0, body.Len()), body.Bytes()...)
	return c.sendVirtualChannelData(c.cliprdrChannelID, pdu)
}

func (c *Client) sendFormatDataResponse(text string, ok bool) error {
	flags := uint16(cbResponseFail)
	var body []byte
	if ok {
		flags = cbResponseOK
		body = utf16NullTerminated(text)
	}
	pdu := append(cliprdrHeader(cbFormatDataResponse, flags, len(body)), body...)
	return c.sendVirtualChannelData(c.cliprdrChannelID, pdu)
}

// handleCliprdrPDU dispatches one complete (reassembled) clipboard PDU.
func (c *Client) handleCliprdrPDU(data []byte, sink FramebufferSink) error {
	if len(data) < 8 {
		return protoErrf("cliprdr PDU too short (%d bytes)", len(data))
	}
	msgType := uint16(data[0]) | uint16(data[1])<<8
	body := data[8:]

	switch msgType {
	case cbMonitorReady:
		// Server signals the channel is ready. Announce our (minimal)
		// capabilities, then our current format list — empty, since
		// Lupinus only pushes clipboard content on explicit user action
		// (the "Sync Clipboard" button), not by watching the local
		// clipboard continuously.
		if err := c.sendClipCaps(); err != nil {
			return err
		}
		return c.sendFormatList(false)

	case cbFormatList:
		// The remote clipboard changed. Must ack regardless of whether we
		// want the content, then — mirroring VNC's server-pushed CutText
		// behavior — proactively fetch it if CF_UNICODETEXT was offered.
		if err := c.sendFormatListResponse(true); err != nil {
			return err
		}
		if !cliprdrFormatListHasUnicodeText(body) {
			return nil
		}
		return c.sendFormatDataRequest(cfUnicodeText)

	case cbFormatDataRequest:
		if len(body) < 4 {
			return c.sendFormatDataResponse("", false)
		}
		formatID := uint32(body[0]) | uint32(body[1])<<8 | uint32(body[2])<<16 | uint32(body[3])<<24
		c.clipMu.Lock()
		text := c.clipText
		c.clipMu.Unlock()
		return c.sendFormatDataResponse(text, formatID == cfUnicodeText)

	case cbFormatDataResponse:
		text := utf16DecodeNullTerminated(body)
		sink.CutText(text)
		return nil

	default:
		// CB_FORMAT_LIST_RESPONSE / CB_CLIP_CAPS / anything else this
		// client doesn't act on — already fully consumed by reassembly,
		// safe to ignore.
		return nil
	}
}

// cliprdrFormatListHasUnicodeText scans a (short-format-name) Format List
// PDU body for a CF_UNICODETEXT entry. Tolerates the long-format-name
// variant too by falling back to a coarse scan if the short-name framing
// doesn't divide evenly, rather than erroring on a server that ignored our
// capabilities and used long names anyway.
func cliprdrFormatListHasUnicodeText(body []byte) bool {
	if len(body)%36 == 0 {
		for i := 0; i+4 <= len(body); i += 36 {
			id := uint32(body[i]) | uint32(body[i+1])<<8 | uint32(body[i+2])<<16 | uint32(body[i+3])<<24
			if id == cfUnicodeText {
				return true
			}
		}
		return false
	}
	// Long format names: formatId(4) + null-terminated UTF-16LE name,
	// variable length. Just look for a 4-byte little-endian 13 followed
	// immediately by a UTF-16 null terminator, which is what an empty
	// name for a well-known format looks like.
	for i := 0; i+6 <= len(body); i++ {
		id := uint32(body[i]) | uint32(body[i+1])<<8 | uint32(body[i+2])<<16 | uint32(body[i+3])<<24
		if id == cfUnicodeText && body[i+4] == 0 && body[i+5] == 0 {
			return true
		}
	}
	return false
}

func utf16DecodeNullTerminated(b []byte) string {
	units := make([]uint16, 0, len(b)/2)
	for i := 0; i+2 <= len(b); i += 2 {
		u := uint16(b[i]) | uint16(b[i+1])<<8
		if u == 0 {
			break
		}
		units = append(units, u)
	}
	return string(utf16.Decode(units))
}
