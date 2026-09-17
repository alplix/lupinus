package rdp

import (
	"bytes"
	"fmt"
	"os"
)

var osStderr = os.Stderr

// Default desktop size requested during GCC — used until the app-level
// caller can configure it; matches the framebuffer size Lupinus asks for.
const defaultWidth, defaultHeight = 1024, 768

// mcsConnect performs the MCS/GCC session setup (MS-RDPBCGR 1.3.1.1):
// Connect-Initial/Connect-Response (BER-encoded, wrapping the GCC
// Conference Create Request/Response from gcc.go), then Erect Domain,
// Attach User, and Channel Join for both the I/O channel and this user's
// own dedicated channel.
func (c *Client) mcsConnect() error {
	if err := c.sendConnectInitial(); err != nil {
		return err
	}
	if err := c.readConnectResponse(); err != nil {
		return err
	}
	if err := c.sendDomainPDU(erectDomainRequestBody()); err != nil {
		return err
	}
	if err := c.sendDomainPDU([]byte{mcsChoiceAttachUserRequest << 2}); err != nil {
		return err
	}
	if err := c.readAttachUserConfirm(); err != nil {
		return err
	}
	if err := c.joinChannel(c.mcsUserID); err != nil { // own user channel
		return err
	}
	if err := c.joinChannel(c.ioChannelID); err != nil {
		return err
	}
	if c.cliprdrChannelID != 0 {
		if err := c.joinChannel(c.cliprdrChannelID); err != nil {
			// Not fatal: fall back to no clipboard sync rather than
			// failing the whole connection over an optional channel.
			c.cliprdrChannelID = 0
		}
	}
	return nil
}

func erectDomainRequestBody() []byte {
	// ErectDomainRequest ::= SEQUENCE { subHeight INTEGER, subInterval INTEGER }
	// both encoded as a 1-byte PER length-determinant + 1-byte value (0).
	return []byte{mcsChoiceErectDomainRequest << 2, 0x01, 0x00, 0x01, 0x00}
}

func (c *Client) sendConnectInitial() error {
	gccData := buildGCCConferenceCreateRequest(defaultWidth, defaultHeight)

	var body bytes.Buffer
	body.Write([]byte{berTagOctetString, 1, 1}) // callingDomainSelector = 0x01
	body.Write([]byte{berTagOctetString, 1, 1}) // calledDomainSelector = 0x01
	body.Write([]byte{berTagBoolean, 1, 0xFF})  // upwardFlag = TRUE

	body.Write(domainParameters(34, 2, 0, 1, 0, 1, 0xFFFF, 2))               // targetParameters
	body.Write(domainParameters(1, 1, 1, 1, 0, 1, 0x420, 2))                 // minimumParameters
	body.Write(domainParameters(0xFFFF, 0xFFFF, 0xFFFF, 1, 0, 1, 0xFFFF, 2)) // maximumParameters

	berTLV(&body, berTagOctetString, gccData) // userData

	var pdu bytes.Buffer
	pdu.Write(berApplicationTag(101)) // Connect-Initial
	pdu.Write(berLength(body.Len()))
	pdu.Write(body.Bytes())

	return writeX224Data(c.tc, pdu.Bytes())
}

func domainParameters(maxChannelIDs, maxUserIDs, maxTokenIDs, numPriorities, minThroughput, maxHeight, maxMCSPDUSize, protocolVersion uint32) []byte {
	var seq bytes.Buffer
	for _, v := range []uint32{maxChannelIDs, maxUserIDs, maxTokenIDs, numPriorities, minThroughput, maxHeight, maxMCSPDUSize, protocolVersion} {
		berTLV(&seq, berTagInteger, berInteger(v))
	}
	var out bytes.Buffer
	berTLV(&out, berTagSequence, seq.Bytes())
	return out.Bytes()
}

func (c *Client) readConnectResponse() error {
	payload, err := readX224Data(c.tc)
	if err != nil {
		return err
	}
	d := newBERDecoder(payload)
	tag, err := d.readTag()
	if err != nil {
		return err
	}
	if tag != 0x7F66 { // [APPLICATION 102] Connect-Response
		return protoErrf("expected MCS Connect-Response, got BER tag 0x%x", tag)
	}
	if _, err := d.readLength(); err != nil {
		return err
	}

	// result (ENUMERATED), calledConnectId (INTEGER), domainParameters
	// (SEQUENCE), then userData (OCTET STRING) — read and discard the
	// first three, we don't need their contents.
	for i := 0; i < 3; i++ {
		if _, _, err := d.readTLV(); err != nil {
			return err
		}
	}
	_, userData, err := d.readTLV()
	if err != nil {
		return err
	}

	return c.parseGCCConferenceCreateResponse(userData)
}

// sendDomainPDU wraps a plain (non-Send-Data) MCS Domain PDU in TPKT and
// writes it — used for Erect Domain Request and Attach User Request,
// which (unlike application data) aren't wrapped in a Send Data Request.
func (c *Client) sendDomainPDU(body []byte) error {
	return writeX224Data(c.tc, body)
}

func (c *Client) readAttachUserConfirm() error {
	payload, err := readX224Data(c.tc)
	if err != nil {
		return err
	}
	if len(payload) < 4 {
		return protoErrf("attach user confirm too short (%d bytes)", len(payload))
	}
	choice := payload[0] >> 2
	if int(choice) != mcsChoiceAttachUserConfirm {
		return protoErrf("expected AttachUserConfirm (choice %d), got %d", mcsChoiceAttachUserConfirm, choice)
	}
	result := payload[1]
	if result != 0 {
		return protoErrf("AttachUserConfirm: server returned result code %d", result)
	}
	c.mcsUserID = uint16(payload[2])<<8 | uint16(payload[3])
	return nil
}

// sendMCSData wraps application-layer data (Client Info PDU, capability
// exchange, and everything else after setup) in an MCS Send Data Request
// and writes it, addressed to the given channel (almost always the I/O
// channel, c.ioChannelID).
func (c *Client) sendMCSData(channelID uint16, data []byte) error {
	if debugWire {
		fmt.Fprintf(osStderr, "DEBUG sendMCSData channel=%d len=%d: % x\n", channelID, len(data), data)
	}
	var pdu bytes.Buffer
	pdu.WriteByte(mcsChoiceSendDataRequest << 2)
	writeUint16BE(&pdu, c.mcsUserID)
	writeUint16BE(&pdu, channelID)
	pdu.WriteByte(0x70) // dataPriority=high, segmentation=begin|end (this client never segments)
	writePERLength(&pdu, len(data))
	pdu.Write(data)
	return writeX224Data(c.tc, pdu.Bytes())
}

// readMCSData reads one MCS Send Data Indication and returns its channel
// ID and payload.
func (c *Client) readMCSData() (uint16, []byte, error) {
	payload, err := readX224Data(c.tc)
	if err != nil {
		return 0, nil, err
	}
	if len(payload) < 6 {
		return 0, nil, protoErrf("MCS send data indication too short (%d bytes): % x", len(payload), payload)
	}
	choice := payload[0] >> 2
	if int(choice) != mcsChoiceSendDataIndication {
		return 0, nil, protoErrf("expected SendDataIndication (choice %d), got %d", mcsChoiceSendDataIndication, choice)
	}
	channelID := uint16(payload[3])<<8 | uint16(payload[4])
	rest := payload[6:] // past choice, initiator(2), channelId(2), dataPriority(1)
	length, n, err := readPERLength(rest)
	if err != nil {
		return 0, nil, err
	}
	rest = rest[n:]
	if length > len(rest) {
		return 0, nil, protoErrf("MCS send data indication: declared length %d exceeds remaining %d bytes", length, len(rest))
	}
	if debugWire {
		fmt.Fprintf(osStderr, "DEBUG readMCSData channel=%d declaredLen=%d actualRemaining=%d full=% x\n", channelID, length, len(rest), rest[:length])
	}
	return channelID, rest[:length], nil
}

// writePERLength writes the MCS/PER length-determinant convention used
// for Send Data Request/Indication's userData: values under 0x80 as a
// single byte, larger values as two bytes with the top bit of the first
// set (this client never sends >0x7FFF in one PDU, so the rarer 4-byte
// form isn't implemented).
func writePERLength(w *bytes.Buffer, n int) {
	if n < 0x80 {
		w.WriteByte(byte(n))
		return
	}
	w.WriteByte(0x80 | byte(n>>8))
	w.WriteByte(byte(n))
}

func readPERLength(b []byte) (length, consumed int, err error) {
	if len(b) < 1 {
		return 0, 0, protoErrf("PER length: no data")
	}
	if b[0]&0x80 == 0 {
		return int(b[0]), 1, nil
	}
	if len(b) < 2 {
		return 0, 0, protoErrf("PER length: truncated 2-byte form")
	}
	return int(b[0]&0x7F)<<8 | int(b[1]), 2, nil
}

func (c *Client) joinChannel(channelID uint16) error {
	var req bytes.Buffer
	req.WriteByte(mcsChoiceChannelJoinRequest << 2)
	writeUint16BE(&req, c.mcsUserID)
	writeUint16BE(&req, channelID)
	if err := c.sendDomainPDU(req.Bytes()); err != nil {
		return err
	}

	payload, err := readX224Data(c.tc)
	if err != nil {
		return err
	}
	if len(payload) < 2 {
		return protoErrf("channel join confirm too short (%d bytes)", len(payload))
	}
	choice := payload[0] >> 2
	if int(choice) != mcsChoiceChannelJoinConfirm {
		return protoErrf("expected ChannelJoinConfirm (choice %d), got %d", mcsChoiceChannelJoinConfirm, choice)
	}
	if result := payload[1]; result != 0 {
		return protoErrf("ChannelJoinConfirm for channel %d: server returned result code %d", channelID, result)
	}
	return nil
}
