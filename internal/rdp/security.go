package rdp

import "bytes"

// TS_SECURITY_HEADER flags (MS-RDPBCGR 2.2.8.1.1.2.1). This client never
// uses RDP's own encryption (Enhanced/TLS security handles that), so the
// only flag that ever appears is SEC_INFO_PKT on the Client Info PDU.
const secInfoPkt uint16 = 0x0040

// TS_INFO_PACKET flags (MS-RDPBCGR 2.2.1.11.1.1).
const (
	infoMouse             = 0x00000001
	infoDisableCtrlAltDel = 0x00000002
	infoUnicode           = 0x00000010
	infoMaximizeShell     = 0x00000020
	infoEnableWindowsKey  = 0x00000100
)

// sendClientInfo sends the Client Info PDU (MS-RDPBCGR 2.2.1.11):
// username/password/domain plus basic client flags and the extended info
// block (client address/timezone/performance flags), over the now-TLS-
// secured I/O channel. No RDP-level encryption header/signature is
// needed since TLS already protects the whole connection.
func (c *Client) sendClientInfo() error {
	var body bytes.Buffer
	writeUint16LE(&body, secInfoPkt)
	writeUint16LE(&body, 0) // flagsHi: unused, always present

	domain := utf16NullTerminated(c.domain)
	username := utf16NullTerminated(c.username)
	password := utf16NullTerminated(c.password)

	writeUint32LE(&body, 0) // CodePage
	writeUint32LE(&body, infoMouse|infoDisableCtrlAltDel|infoUnicode|infoMaximizeShell|infoEnableWindowsKey)
	writeUint16LE(&body, uint16(len(domain)-2))   // cbDomain: excludes the null terminator
	writeUint16LE(&body, uint16(len(username)-2)) // cbUserName
	writeUint16LE(&body, uint16(len(password)-2)) // cbPassword
	writeUint16LE(&body, 0)                       // cbAlternateShell
	writeUint16LE(&body, 0)                       // cbWorkingDir
	body.Write(domain)
	body.Write(username)
	body.Write(password)
	writeUint16LE(&body, 0) // AlternateShell: empty, just the null terminator
	writeUint16LE(&body, 0) // WorkingDir: empty, just the null terminator

	writeExtendedInfo(&body)

	return c.sendMCSData(c.ioChannelID, body.Bytes())
}

// writeExtendedInfo appends TS_EXTENDED_INFO_PACKET (MS-RDPBCGR
// 2.2.1.11.1.1.1). The timezone block is the fixed-size TS_TIME_ZONE_
// INFORMATION structure — zero-filled (UTC, no daylight rule) rather than
// populated from the host's real timezone, which every server tolerates
// fine.
func writeExtendedInfo(body *bytes.Buffer) {
	addr := utf16NullTerminated("0.0.0.0")
	dir := utf16NullTerminated("")

	writeUint16LE(body, 2) // clientAddressFamily: AF_INET
	writeUint16LE(body, uint16(len(addr)))
	body.Write(addr)
	writeUint16LE(body, uint16(len(dir)))
	body.Write(dir)
	body.Write(make([]byte, 172)) // TS_TIME_ZONE_INFORMATION, zero-filled
	writeUint32LE(body, 0)        // clientSessionId (ignored by servers)
	// performanceFlags: disable the usual eye-candy for a snappier session
	// over what may be a slow link — wallpaper, full-window-drag, menu/
	// task animations, theming.
	writeUint32LE(body, 0x0000000F)
	writeUint16LE(body, 0) // cbAutoReconnectCookie: none
}

func utf16NullTerminated(s string) []byte {
	out := make([]byte, 0, len(s)*2+2)
	for _, r := range s {
		out = append(out, byte(r), byte(r>>8))
	}
	return append(out, 0, 0)
}

// Licensing PDU types (MS-RDPBCGR 2.2.1.12) that arrive on the I/O
// channel, wrapped in a TS_SECURITY_HEADER with SEC_LICENSE_PKT set,
// before the capability exchange begins.
const (
	secLicensePkt uint16 = 0x0080

	licenseErrorAlert = 0xFF
)

// handleLicensing reads the server's first post-Client-Info PDU on the
// I/O channel. Almost every real server (including xrdp, and Windows with
// a valid/grace-period license) sends a "License Error (valid client)"
// alert and moves straight on; full license negotiation/storage isn't
// implemented, so anything else is a clear unsupported error rather than
// a confusing hang or parse failure.
func (c *Client) handleLicensing() error {
	_, data, err := c.readSecurityPDU()
	if err != nil {
		return err
	}
	if len(data) < 1 {
		return protoErrf("licensing PDU: empty payload")
	}
	if data[0] != licenseErrorAlert {
		return &UnsupportedError{Msg: "server requires full RDP license negotiation, not implemented"}
	}
	// TS_LICENSE_ERROR_MESSAGE body (validClientMessage, etc.) isn't
	// otherwise interesting for a valid-client short-circuit — nothing
	// more to do, capability exchange starts next.
	return nil
}

// readSecurityPDU reads one MCS data unit on the I/O channel and strips
// its TS_SECURITY_HEADER, returning the header flags and the remaining
// payload.
func (c *Client) readSecurityPDU() (flags uint16, payload []byte, err error) {
	_, data, err := c.readMCSData()
	if err != nil {
		return 0, nil, err
	}
	if len(data) < 4 {
		return 0, nil, protoErrf("security PDU too short (%d bytes)", len(data))
	}
	flags = uint16(data[0]) | uint16(data[1])<<8
	return flags, data[4:], nil // skip flags + flagsHi
}
