package rdp

import (
	"bytes"
	"fmt"
)

// TS_SHAREDATAHEADER pduType2 values (MS-RDPBCGR 2.2.8.1.1.1.2).
const (
	pduType2Synchronize = 31
	pduType2Control     = 20
	pduType2FontList    = 39
	pduType2FontMap     = 40
)

// Control PDU actions (MS-RDPBCGR 2.2.1.15.1).
const (
	ctrlActionCooperate      = 1
	ctrlActionRequestControl = 2
)

// finalize runs the Connection Finalization sequence (MS-RDPBCGR
// 1.3.1.1): Synchronize, Control (Cooperate, then Request Control), Font
// List. Persistent Key List isn't sent (this client never populates a
// bitmap cache to persist, see the project plan). After this the session
// is live and Run's Fast-Path loop can start.
//
// The three client PDUs are sent back-to-back with no read in between —
// confirmed against a real server's source (xrdp's
// xrdp_rdp_process_data_sync is a straight no-op, and Control/Cooperate
// falls through its "action is unknown, skipped" branch; only Control/
// RequestControl actually triggers a reply, and that reply is a burst of
// three PDUs — Synchronize, Control/Cooperate, Control/GrantedControl —
// not a one-for-one reply to each client message. An earlier version of
// this function read a reply after every send and hung forever waiting
// for a Synchronize/Cooperate acknowledgement that servers never send.
func (c *Client) finalize() error {
	dbg := func(step string) {
		if debugWire {
			fmt.Fprintf(osStderr, "DEBUG finalize: %s\n", step)
		}
	}

	dbg("send Synchronize")
	if err := c.sendSynchronize(); err != nil {
		return err
	}
	dbg("send Control Cooperate")
	if err := c.sendControl(ctrlActionCooperate); err != nil {
		return err
	}
	dbg("send Control RequestControl")
	if err := c.sendControl(ctrlActionRequestControl); err != nil {
		return err
	}

	// Server's reply burst: Synchronize, Control/Cooperate, Control/
	// GrantedControl. Read three Data PDUs, tolerant of exact pduType2
	// (servers are consistent here, but this isn't worth hanging over).
	for i := 0; i < 3; i++ {
		dbg(fmt.Sprintf("read activation reply %d/3", i+1))
		if err := c.readShareDataPDU(0); err != nil {
			return err
		}
	}

	dbg("send FontList")
	if err := c.sendFontList(); err != nil {
		return err
	}
	dbg("read FontMap reply")
	err := c.readShareDataPDU(pduType2FontMap)
	dbg("finalize done")
	return err
}

func (c *Client) sendSynchronize() error {
	var b bytes.Buffer
	writeUint16LE(&b, 1)               // messageType (fixed)
	writeUint16LE(&b, serverChannelID) // targetUser
	return c.sendShareDataPDU(pduType2Synchronize, b.Bytes())
}

func (c *Client) sendControl(action uint16) error {
	var b bytes.Buffer
	writeUint16LE(&b, action)
	writeUint16LE(&b, 0) // grantId
	writeUint32LE(&b, 0) // controlId
	return c.sendShareDataPDU(pduType2Control, b.Bytes())
}

func (c *Client) sendFontList() error {
	var b bytes.Buffer
	writeUint16LE(&b, 0)      // numberFonts
	writeUint16LE(&b, 0)      // totalNumFonts
	writeUint16LE(&b, 0x0003) // listFlags: FONTLIST_FIRST | FONTLIST_LAST
	writeUint16LE(&b, 0x0032) // entrySize (fixed legacy value)
	return c.sendShareDataPDU(pduType2FontList, b.Bytes())
}

// sendShareDataPDU wraps body in a TS_SHAREDATAHEADER (itself inside a
// TS_SHARECONTROLHEADER Data PDU) and sends it — the envelope every
// finalization and later in-session client PDU uses.
func (c *Client) sendShareDataPDU(pduType2 uint8, body []byte) error {
	var data bytes.Buffer
	writeUint32LE(&data, c.shareID)
	writeUint8(&data, 0)                      // pad1
	writeUint8(&data, 1)                      // streamId: STREAM_LOW (fixed, no multi-stream use)
	writeUint16LE(&data, uint16(len(body)+6)) // uncompressedLength: this header's data-length fields + body
	writeUint8(&data, pduType2)
	writeUint8(&data, 0) // compressedType: not compressed
	writeUint16LE(&data, 0)
	data.Write(body)
	return c.sendShareControlPDU(pduTypeData, data.Bytes())
}

// readShareDataPDU reads one Data PDU and, if wantType2 is non-zero,
// checks its pduType2 matches what's expected. Passing 0 skips that
// check — used for the activation reply burst, where this client reads
// three PDUs without being strict about which order a server sends
// Synchronize/Control/Cooperate/GrantedControl in.
func (c *Client) readShareDataPDU(wantType2 uint8) error {
	_, data, err := c.readMCSData()
	if err != nil {
		return err
	}
	if len(data) < 6+12 {
		return protoErrf("share data PDU too short (%d bytes)", len(data))
	}
	pduType := (uint16(data[2]) | uint16(data[3])<<8) & 0x0F
	if pduType != pduTypeData {
		return protoErrf("expected Data PDU (type %d), got %d", pduTypeData, pduType)
	}
	// ShareControlHeader is 6 bytes (totalLength, pduType, pduSource);
	// within the ShareDataHeader that follows, shareId(4)+pad1(1)+
	// streamId(1)+uncompressedLength(2) precede pduType2.
	gotType2 := data[6+8]
	if wantType2 != 0 && gotType2 != wantType2 {
		return protoErrf("expected pduType2 %d, got %d", wantType2, gotType2)
	}
	return nil
}
