package rdp

import "bytes"

// TS_SHARECONTROLHEADER PDU types (MS-RDPBCGR 2.2.8.1.1.1.1).
const (
	pduTypeDemandActive  = 1
	pduTypeConfirmActive = 3
	pduTypeData          = 7
)

// Capability set types (MS-RDPBCGR 2.2.1.13.1.1.1) this client declares.
const (
	capsGeneral        = 1
	capsBitmap         = 2
	capsOrder          = 3
	capsShare          = 9
	capsColorCache     = 10
	capsPointer        = 8
	capsInput          = 13
	capsFont           = 14
	capsVirtualChannel = 20
)

// serverChannelID is the fixed "originator" value every client sends back
// in Confirm Active — a conventional constant (MCS_GLOBAL_CHANNEL) used
// across RDP client implementations, not something negotiated.
const serverChannelID = 0x03EA

// capabilityExchange reads the server's Demand Active PDU (just enough to
// get the share ID — this client doesn't otherwise adapt to the server's
// declared capabilities in v0.2.0) and replies with Confirm Active
// carrying a minimal-but-valid capability set list: General, Bitmap
// (requesting 32bpp), Order (declaring support for none — every server
// falls back to bitmap updates for a client that supports no orders),
// Pointer, and Input (requesting Fast-Path input).
func (c *Client) capabilityExchange() error {
	if err := c.readDemandActive(); err != nil {
		return err
	}
	return c.sendConfirmActive()
}

func (c *Client) readDemandActive() error {
	_, data, err := c.readMCSData()
	if err != nil {
		return err
	}
	if len(data) < 6 {
		return protoErrf("share control header too short (%d bytes)", len(data))
	}
	pduType := (uint16(data[2]) | uint16(data[3])<<8) & 0x0F
	if pduType != pduTypeDemandActive {
		return protoErrf("expected Demand Active PDU (type %d), got %d", pduTypeDemandActive, pduType)
	}
	body := data[6:] // past totalLength(2) + pduType(2) + pduSource(2)
	if len(body) < 4 {
		return protoErrf("demand active PDU too short (%d bytes)", len(body))
	}
	c.shareID = uint32(body[0]) | uint32(body[1])<<8 | uint32(body[2])<<16 | uint32(body[3])<<24
	// The rest (sourceDescriptor, the server's own capability sets,
	// sessionId) isn't parsed in v0.2.0 — this client doesn't adapt its
	// behavior based on what the server advertises there.
	return nil
}

func (c *Client) sendConfirmActive() error {
	caps := bytes.Buffer{}
	numCaps := 0
	for _, capSet := range [][]byte{
		generalCapabilitySet(),
		bitmapCapabilitySet(),
		orderCapabilitySet(),
		pointerCapabilitySet(),
		inputCapabilitySet(),
		shareCapabilitySet(),
		colorCacheCapabilitySet(),
		fontCapabilitySet(),
		virtualChannelCapabilitySet(),
	} {
		caps.Write(capSet)
		numCaps++
	}

	source := []byte("Lupinus\x00")

	var body bytes.Buffer
	writeUint32LE(&body, c.shareID)
	writeUint16LE(&body, serverChannelID) // originatorId
	writeUint16LE(&body, uint16(len(source)))
	writeUint16LE(&body, uint16(caps.Len()+4)) // lengthCombinedCapabilities: numberCapabilities + pad2Octets + capabilitySets, per MS-RDPBCGR 2.2.1.13.2.1
	body.Write(source)
	writeUint16LE(&body, uint16(numCaps))
	writeUint16LE(&body, 0) // pad2Octets
	body.Write(caps.Bytes())

	return c.sendShareControlPDU(pduTypeConfirmActive, body.Bytes())
}

// sendShareControlPDU prepends a TS_SHARECONTROLHEADER and sends the
// result as one MCS data unit — used for Confirm Active and (in
// finalize.go) the finalization Data PDUs.
//
// TS_SHARECONTROLHEADER is THREE separate 16-bit fields (confirmed
// against xrdp's own source, libxrdp/xrdp_rdp.c: it reads totalLength,
// then pduType, then unconditionally reads two more bytes for pduSource
// before handing off to the body parser) — not totalLength followed by a
// single packed pduType/pduSource word as an earlier reading of this
// client's own (mis-remembered) notes assumed. Getting this wrong shifts
// every byte after the header by 2, which is exactly what caused a real
// server to read garbage for everything from Confirm Active onward while
// this client's own self-consistency tests (which reproduced the same 2-
// byte-short assumption) kept passing.
func (c *Client) sendShareControlPDU(pduType uint16, body []byte) error {
	pduSource := mcsUserChannelBase + uint16(c.mcsUserID)
	var pdu bytes.Buffer
	writeUint16LE(&pdu, uint16(6+len(body)))
	writeUint16LE(&pdu, 0x10|pduType) // pduType: PDUVersion(1, high nibble) | type (low nibble)
	writeUint16LE(&pdu, pduSource)
	pdu.Write(body)
	return c.sendMCSData(c.ioChannelID, pdu.Bytes())
}

func capSetHeader(capType uint16, body []byte) []byte {
	var b bytes.Buffer
	writeUint16LE(&b, capType)
	writeUint16LE(&b, uint16(4+len(body)))
	b.Write(body)
	return b.Bytes()
}

func generalCapabilitySet() []byte {
	var b bytes.Buffer
	writeUint16LE(&b, 1)      // osMajorType: WINDOWS
	writeUint16LE(&b, 0)      // osMinorType: unspecified
	writeUint16LE(&b, 0x0200) // protocolVersion (fixed)
	writeUint16LE(&b, 0)      // pad2octetsA
	writeUint16LE(&b, 0)      // generalCompressionTypes
	writeUint16LE(&b, 0x0001) // extraFlags: FASTPATH_OUTPUT_SUPPORTED — we want Fast-Path server updates
	writeUint16LE(&b, 0)      // updateCapabilityFlag
	writeUint16LE(&b, 0)      // remoteUnshareFlag
	writeUint16LE(&b, 0)      // generalCompressionLevel
	writeUint8(&b, 0)         // refreshRectSupport
	writeUint8(&b, 0)         // suppressOutputSupport
	return capSetHeader(capsGeneral, b.Bytes())
}

func bitmapCapabilitySet() []byte {
	var b bytes.Buffer
	writeUint16LE(&b, 32) // preferredBitsPerPixel: ask for 32bpp; decode handles whatever the server actually uses
	writeUint16LE(&b, 1)  // receive1BitPerPixel (legacy, ignored by modern servers)
	writeUint16LE(&b, 1)  // receive4BitsPerPixel
	writeUint16LE(&b, 1)  // receive8BitsPerPixel
	writeUint16LE(&b, defaultWidth)
	writeUint16LE(&b, defaultHeight)
	writeUint16LE(&b, 0) // pad2octets
	writeUint16LE(&b, 0) // desktopResizeFlag: not supported in v0.2.0
	writeUint16LE(&b, 1) // bitmapCompressionFlag: TRUE — we implement Interleaved RLE
	writeUint8(&b, 0)    // highColorFlags (obsolete)
	writeUint8(&b, 0)    // drawingFlags
	writeUint16LE(&b, 1) // multipleRectangleSupport: TRUE
	writeUint16LE(&b, 0) // pad2octetsB
	return capSetHeader(capsBitmap, b.Bytes())
}

// orderCapabilitySet declares support for zero GDI drawing orders — every
// server falls back to sending bitmap updates for a client shaped this
// way, which is exactly what v0.2.0 wants (see the project plan).
func orderCapabilitySet() []byte {
	var b bytes.Buffer
	b.Write(make([]byte, 16)) // terminalDescriptor
	writeUint32LE(&b, 0)      // pad4octetsA
	writeUint16LE(&b, 1)      // desktopSaveXGranularity
	writeUint16LE(&b, 20)     // desktopSaveYGranularity
	writeUint16LE(&b, 0)      // pad2octetsA
	writeUint16LE(&b, 1)      // maximumOrderLevel
	writeUint16LE(&b, 0)      // numberFonts
	writeUint16LE(&b, 0x0022) // orderFlags: NEGOTIATEORDERSUPPORT | ZEROBOUNDSDELTASSUPPORT
	b.Write(make([]byte, 32)) // orderSupport: all zero, no orders supported
	writeUint16LE(&b, 0)      // textFlags
	writeUint16LE(&b, 0)      // orderSupportExFlags
	writeUint32LE(&b, 0)      // pad4octetsB
	writeUint32LE(&b, 230400) // desktopSaveSize (legacy, unused)
	writeUint16LE(&b, 0)      // pad2octetsC
	writeUint16LE(&b, 0)      // pad2octetsD
	writeUint16LE(&b, 0)      // textANSICodePage
	writeUint16LE(&b, 0)      // pad2octetsE
	return capSetHeader(capsOrder, b.Bytes())
}

func pointerCapabilitySet() []byte {
	var b bytes.Buffer
	writeUint16LE(&b, 1)  // colorPointerFlag: TRUE
	writeUint16LE(&b, 20) // colorPointerCacheSize
	writeUint16LE(&b, 20) // pointerCacheSize
	return capSetHeader(capsPointer, b.Bytes())
}

func shareCapabilitySet() []byte {
	var b bytes.Buffer
	writeUint16LE(&b, 0) // nodeId: unused for a single-user session
	writeUint16LE(&b, 0) // pad2octets
	return capSetHeader(capsShare, b.Bytes())
}

func colorCacheCapabilitySet() []byte {
	var b bytes.Buffer
	writeUint16LE(&b, 6) // colorTableCacheSize (fixed conventional value)
	writeUint16LE(&b, 0) // pad2octets
	return capSetHeader(capsColorCache, b.Bytes())
}

func fontCapabilitySet() []byte {
	var b bytes.Buffer
	writeUint16LE(&b, 1) // fontSupportFlags: FONTSUPPORT_FONTLIST
	writeUint16LE(&b, 0) // pad2octets
	return capSetHeader(capsFont, b.Bytes())
}

// virtualChannelCapabilitySet declares support for the virtual channel
// mechanism itself (required by some servers even though v0.2.0 requests
// zero actual channels in Client Network Data) with no compression.
func virtualChannelCapabilitySet() []byte {
	var b bytes.Buffer
	writeUint32LE(&b, 0) // flags: VCCAPS_NO_COMPR
	return capSetHeader(capsVirtualChannel, b.Bytes())
}

func inputCapabilitySet() []byte {
	var b bytes.Buffer
	// INPUT_FLAG_SCANCODES | INPUT_FLAG_FASTPATH_INPUT | INPUT_FLAG_FASTPATH_INPUT2
	writeUint16LE(&b, 0x0029)
	writeUint16LE(&b, 0)          // pad2octetsA
	writeUint32LE(&b, 0x00000409) // keyboardLayout: US English (matches Client Core Data)
	writeUint32LE(&b, 4)          // keyboardType: IBM enhanced
	writeUint32LE(&b, 0)          // keyboardSubType
	writeUint32LE(&b, 12)         // keyboardFunctionKey
	b.Write(make([]byte, 64))     // imeFileName
	return capSetHeader(capsInput, b.Bytes())
}
