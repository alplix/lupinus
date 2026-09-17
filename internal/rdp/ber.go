package rdp

import "bytes"

// Minimal BER (X.690) encoding/decoding helpers — just the subset MCS's
// Connect-Initial/Connect-Response PDUs (T.125) actually use: SEQUENCE,
// INTEGER, BOOLEAN, OCTET STRING and the two application-class tags for
// the PDUs themselves. Not a general ASN.1 library.

func berLength(n int) []byte {
	switch {
	case n < 0x80:
		return []byte{byte(n)}
	case n <= 0xFF:
		return []byte{0x81, byte(n)}
	default:
		return []byte{0x82, byte(n >> 8), byte(n)}
	}
}

// berTLV writes tag, BER length of value, then value.
func berTLV(buf *bytes.Buffer, tag byte, value []byte) {
	buf.WriteByte(tag)
	buf.Write(berLength(len(value)))
	buf.Write(value)
}

// berInteger encodes an unsigned integer using the minimum number of
// bytes, prefixing a 0x00 if the high bit would otherwise make it look
// negative — matches what every RDP server implementation expects for the
// DomainParameters fields MCS uses this for.
func berInteger(v uint32) []byte {
	var b []byte
	switch {
	case v <= 0xFF:
		b = []byte{byte(v)}
	case v <= 0xFFFF:
		b = []byte{byte(v >> 8), byte(v)}
	case v <= 0xFFFFFF:
		b = []byte{byte(v >> 16), byte(v >> 8), byte(v)}
	default:
		b = []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
	}
	if b[0]&0x80 != 0 {
		b = append([]byte{0}, b...)
	}
	return b
}

const (
	berTagBoolean     = 0x01
	berTagInteger     = 0x02
	berTagOctetString = 0x04
	berTagSequence    = 0x30 // constructed
)

// berApplicationTag builds the (possibly multi-byte) leading tag octets
// for an [APPLICATION n] IMPLICIT SEQUENCE, constructed form — the outer
// tag on Connect-Initial (101) and Connect-Response (102).
func berApplicationTag(n byte) []byte {
	return []byte{0x7F, n} // class=APPLICATION|constructed=0x60|0x1F=0x7F, then tag number (fits one byte for 101/102)
}

// berDecoder is a tiny cursor-based reader for the handful of BER shapes
// this client needs to parse out of Connect-Response.
type berDecoder struct {
	buf []byte
	pos int
}

func newBERDecoder(buf []byte) *berDecoder { return &berDecoder{buf: buf} }

func (d *berDecoder) remaining() int { return len(d.buf) - d.pos }

func (d *berDecoder) readByte() (byte, error) {
	if d.pos >= len(d.buf) {
		return 0, protoErrf("BER: unexpected end of data")
	}
	b := d.buf[d.pos]
	d.pos++
	return b, nil
}

// readTag reads a tag octet, handling the multi-byte application-tag form
// (0x7F followed by the real tag number) used by Connect-Response.
func (d *berDecoder) readTag() (int, error) {
	first, err := d.readByte()
	if err != nil {
		return 0, err
	}
	if first&0x1F != 0x1F {
		return int(first), nil
	}
	second, err := d.readByte()
	if err != nil {
		return 0, err
	}
	return int(first)<<8 | int(second), nil
}

func (d *berDecoder) readLength() (int, error) {
	first, err := d.readByte()
	if err != nil {
		return 0, err
	}
	if first&0x80 == 0 {
		return int(first), nil
	}
	n := int(first & 0x7F)
	if n == 0 || n > 4 {
		return 0, protoErrf("BER: unsupported length form (%d bytes)", n)
	}
	length := 0
	for i := 0; i < n; i++ {
		b, err := d.readByte()
		if err != nil {
			return 0, err
		}
		length = length<<8 | int(b)
	}
	return length, nil
}

// readTLV reads a tag + length + value in one step, returning the value
// bytes (the decoder's cursor advances past them).
func (d *berDecoder) readTLV() (tag int, value []byte, err error) {
	tag, err = d.readTag()
	if err != nil {
		return 0, nil, err
	}
	length, err := d.readLength()
	if err != nil {
		return 0, nil, err
	}
	if d.pos+length > len(d.buf) {
		return 0, nil, protoErrf("BER: value length %d exceeds remaining data", length)
	}
	value = d.buf[d.pos : d.pos+length]
	d.pos += length
	return tag, value, nil
}

func berDecodeInteger(v []byte) uint32 {
	var out uint32
	for _, b := range v {
		out = out<<8 | uint32(b)
	}
	return out
}
