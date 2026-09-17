package rdp

import (
	"bytes"
)

// GCC (T.124) Conference Create Request/Response, carried as the opaque
// userData OCTET STRING inside the MCS Connect-Initial/Connect-Response.
//
// The outer T.124 ConferenceCreateRequest shape (PER-encoded) is fixed for
// every RDP client's use of it — conference name "1", default options, a
// single h221NonStandard user-data entry keyed "Duca" carrying the actual
// RDP-specific data blocks. Byte-for-byte verified against FreeRDP's
// gcc_write_conference_create_request (libfreerdp/core/gcc.c) and its PER
// helpers (libfreerdp/crypto/per.c) — worth being this precise about,
// since an earlier hand-rolled version of this wrapper was 6 bytes short
// (17 instead of 23), which xrdp's parser doesn't validate field-by-field
// but DOES unconditionally skip a fixed 23 bytes before reading the first
// real data block, silently misreading every block after that by 6 bytes.
//
// t124CcrOID is T.124's object identifier {0 0 20 124 0 1}, PER-encoded
// per per_write_object_identifier: length(5), (oid[0]*40+oid[1]), oid[2..5].
var t124CcrPrefix = []byte{
	0x00,                               // per_write_choice(0): ConnectGCCPDU/Key choice 0 (object)
	0x05, 0x00, 0x14, 0x7c, 0x00, 0x01, // per_write_object_identifier(t124_02_98_oid)
}

// gccUserDataKeyBody is per_write_choice(0xC0) [UserData present, select
// h221NonStandard] followed by per_write_octet_string("Duca", 4, 4) (which
// has mlength=0, so its own length prefix is a single 0x00 byte).
var gccUserDataKeyBody = []byte{
	0xC0,
	0x00, 0x44, 0x75, 0x63, 0x61, // length(0) + "Duca"
}

// RDP data block types (MS-RDPBCGR 2.2.1.3), each a 2-byte type + 2-byte
// length (both little-endian) header followed by fixed fields — not BER
// or PER encoded, just plain structures.
const (
	dataBlockClientCore     = 0xC001
	dataBlockClientSecurity = 0xC002
	dataBlockClientNetwork  = 0xC003

	dataBlockServerCore     = 0x0C01
	dataBlockServerSecurity = 0x0C02
	dataBlockServerNetwork  = 0x0C03
)

// buildClientCoreData builds the Client Core Data block (MS-RDPBCGR
// 2.2.1.3.2): desktop size, color depth, and the handful of capability
// flags/identifiers real servers expect to see filled in sensibly.
func buildClientCoreData(width, height int) []byte {
	var b bytes.Buffer
	writeUint32LE(&b, 0x00080001) // version: RDP 5.0+ client, standard value every implementation sends
	writeUint16LE(&b, uint16(width))
	writeUint16LE(&b, uint16(height))
	writeUint16LE(&b, 0xCA01)     // colorDepth: RNS_UD_COLOR_8BPP (legacy field, superseded by earlyCapabilityFlags/postBeta2ColorDepth below)
	writeUint16LE(&b, 0xAA03)     // SASSequence (fixed/reserved value used by every client)
	writeUint32LE(&b, 0x00000409) // keyboardLayout: US English — fine as a default; not otherwise surfaced in the UI yet
	writeUint32LE(&b, 2600)       // clientBuild
	// clientName: 16 UTF-16LE code units (32 bytes), null-terminated/padded.
	name := utf16Pad("lupinus", 32)
	b.Write(name)
	writeUint32LE(&b, 4)      // keyboardType: IBM enhanced
	writeUint32LE(&b, 0)      // keyboardSubType
	writeUint32LE(&b, 12)     // keyboardFunctionKey count
	b.Write(make([]byte, 64)) // imeFileName (unused)
	writeUint16LE(&b, 0x0001) // postBeta2ColorDepth: RNS_UD_COLOR_8BPP (again legacy; real depth negotiated via capabilities)
	writeUint16LE(&b, 1)      // clientProductId
	writeUint32LE(&b, 0)      // serialNumber
	writeUint16LE(&b, 0x0018) // highColorDepth: 24bpp — actual bpp still finalized during capability exchange
	writeUint16LE(&b, 0x000F) // supportedColorDepths: 24|16|15|32bpp (bits 0-3) — decode handles whatever the server actually sends
	writeUint16LE(&b, 0x0001) // earlyCapabilityFlags: RNS_UD_CS_SUPPORT_ERRINFO_PDU
	b.Write(make([]byte, 64)) // clientDigProductId (unused)
	writeUint8(&b, 0)         // connectionType (unset)
	writeUint8(&b, 0)         // pad1octet
	writeUint32LE(&b, 0)      // serverSelectedProtocol — filled in by the server side only; 0 here
	return b.Bytes()
}

// buildClientSecurityData builds the Client Security Data block
// (MS-RDPBCGR 2.2.1.4.3). encryptionMethods=0 and extEncryptionMethods=0:
// this client always negotiates Enhanced (TLS) Security, so RDP's own
// legacy encryption is never used.
func buildClientSecurityData() []byte {
	var b bytes.Buffer
	writeUint32LE(&b, 0) // encryptionMethods
	writeUint32LE(&b, 0) // extEncryptionMethods
	return b.Bytes()
}

// buildClientNetworkData builds the Client Network Data block
// (MS-RDPBCGR 2.2.1.3.4). This client requests zero additional virtual
// channels (no clipboard/drive/audio redirection in v0.2.0) — just the
// implicit I/O channel every session gets.
func buildClientNetworkData() []byte {
	var b bytes.Buffer
	writeUint32LE(&b, 0) // channelCount
	return b.Bytes()
}

func gccDataBlock(blockType uint16, payload []byte) []byte {
	var b bytes.Buffer
	writeUint16LE(&b, blockType)
	writeUint16LE(&b, uint16(4+len(payload)))
	b.Write(payload)
	return b.Bytes()
}

// perWriteLength writes a PER length determinant (X.691): a single byte
// for values <= 0x7F, otherwise a 2-byte big-endian value with the top bit
// set — note this is a different convention from the BER length form used
// for the outer MCS Connect-Initial in mcs.go (0x81/0x82 marker bytes):
// PER folds the "long form" flag into the same 16-bit word rather than
// prefixing a separate marker byte, per FreeRDP's per_write_length.
func perWriteLength(buf *bytes.Buffer, length int) {
	if length > 0x7F {
		v := uint16(length) | 0x8000
		buf.WriteByte(byte(v >> 8))
		buf.WriteByte(byte(v))
	} else {
		buf.WriteByte(byte(length))
	}
}

// buildGCCConferenceCreateRequest assembles the full GCC user-data
// payload (Client Core + Security + Network Data) and wraps it in the
// PER-encoded T.124 ConferenceCreateRequest template, matching FreeRDP's
// gcc_write_conference_create_request field-for-field (see t124CcrPrefix's
// doc comment).
func buildGCCConferenceCreateRequest(width, height int) []byte {
	blocks := append(gccDataBlock(dataBlockClientCore, buildClientCoreData(width, height)),
		append(gccDataBlock(dataBlockClientSecurity, buildClientSecurityData()),
			gccDataBlock(dataBlockClientNetwork, buildClientNetworkData())...)...)

	var userData bytes.Buffer // per_write_octet_string(blocks, len(blocks), 0)
	perWriteLength(&userData, len(blocks))
	userData.Write(blocks)

	var inner bytes.Buffer
	inner.WriteByte(0x00)           // per_write_choice(0): ConnectGCCPDU select conferenceCreateRequest
	inner.WriteByte(0x08)           // per_write_selection(0x08): optional userData present
	inner.Write([]byte{0x00, 0x10}) // per_write_numeric_string("1", 1, 1): conferenceName
	inner.WriteByte(0x00)           // per_write_padding(1)
	inner.WriteByte(0x01)           // per_write_number_of_sets(1)
	inner.Write(gccUserDataKeyBody) // choice(0xC0) + octet_string("Duca")
	inner.Write(userData.Bytes())

	var out bytes.Buffer
	out.Write(t124CcrPrefix)
	perWriteLength(&out, inner.Len()) // connectPDU length: bytes following this field
	out.Write(inner.Bytes())
	return out.Bytes()
}

// parseGCCConferenceCreateResponse extracts what this client actually
// needs from the server's GCC user data: the I/O channel ID out of Server
// Network Data. The exact PER header the response is wrapped in isn't
// worth precisely replicating a parser for (unlike the request, which
// this client fully controls) — instead scan for the first well-formed
// Server Core Data block header and parse the sequential data blocks from
// there, which is robust to minor header differences between server
// implementations.
func (c *Client) parseGCCConferenceCreateResponse(userData []byte) error {
	start := -1
	for i := 0; i+4 <= len(userData); i++ {
		if uint16(userData[i])|uint16(userData[i+1])<<8 == dataBlockServerCore {
			start = i
			break
		}
	}
	if start < 0 {
		return protoErrf("GCC Conference Create Response: no Server Core Data block found")
	}

	buf := userData[start:]
	for len(buf) >= 4 {
		blockType := uint16(buf[0]) | uint16(buf[1])<<8
		blockLen := int(uint16(buf[2]) | uint16(buf[3])<<8)
		if blockLen < 4 || blockLen > len(buf) {
			break
		}
		body := buf[4:blockLen]

		if blockType == dataBlockServerNetwork && len(body) >= 4 {
			c.ioChannelID = uint16(body[0]) | uint16(body[1])<<8
		}

		buf = buf[blockLen:]
	}

	if c.ioChannelID == 0 {
		return protoErrf("GCC Conference Create Response: no Server Network Data / I/O channel ID found")
	}
	c.serverWidth, c.serverHeight = defaultWidth, defaultHeight // RDP: client dictates desktop size, server confirms by honoring it
	return nil
}

// utf16Pad encodes s as UTF-16LE, truncated/zero-padded to exactly n
// bytes (matching the fixed-width string fields RDP's Client Core Data
// uses).
func utf16Pad(s string, n int) []byte {
	out := make([]byte, n)
	i := 0
	for _, r := range s {
		if i+2 > n-2 { // leave room for the null terminator
			break
		}
		out[i] = byte(r)
		out[i+1] = byte(r >> 8)
		i += 2
	}
	return out
}
