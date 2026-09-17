package rfb

import (
	"fmt"
)

// handshake performs version negotiation, security (None or VNC
// Authentication), ClientInit/ServerInit, and sends the initial
// SetPixelFormat + SetEncodings. See RFC 6143 §7.1–7.4.
func (c *Client) handshake(username, password string) error {
	serverMajor, serverMinor, err := c.readProtocolVersion()
	if err != nil {
		return err
	}

	// We only ever speak 3.3, 3.7 or 3.8; clamp down to whichever the
	// server advertises, defaulting to 3.8 for anything newer.
	major, minor := 3, 8
	if serverMajor < 3 || (serverMajor == 3 && serverMinor < 7) {
		major, minor = 3, 3
	} else if serverMajor == 3 && serverMinor == 7 {
		major, minor = 3, 7
	}

	if _, err := fmt.Fprintf(c.w, "RFB %03d.%03d\n", major, minor); err != nil {
		return err
	}

	chosen, err := c.negotiateSecurity(major, minor, username, password)
	if err != nil {
		return err
	}

	switch chosen {
	case secVNCAuth:
		if err := c.performVNCAuth(password); err != nil {
			return err
		}
	case secARD:
		if err := c.performARDAuth(username, password); err != nil {
			return err
		}
	}

	// Unlike secNone, both secVNCAuth and secARD produce an actual
	// authentication attempt the server reports success/failure on, even
	// under RFB 3.7 (which otherwise skips SecurityResult entirely).
	sendResult := chosen == secVNCAuth || chosen == secARD || (major == 3 && minor == 8)
	if sendResult {
		result, err := readUint32(c.r)
		if err != nil {
			return err
		}
		if result != 0 {
			reason := ""
			if major == 3 && minor == 8 {
				reasonLen, err := readUint32(c.r)
				if err == nil {
					if b, err2 := readFull(c.r, int(reasonLen)); err2 == nil {
						reason = string(b)
					}
				}
			}
			return &AuthError{Reason: reason}
		}
	}

	// ClientInit: request a shared session so we don't kick other viewers off.
	if err := writeUint8(c.w, 1); err != nil {
		return err
	}

	if err := c.readServerInit(); err != nil {
		return err
	}

	if err := c.sendSetPixelFormat(); err != nil {
		return err
	}
	if err := c.sendSetEncodings(); err != nil {
		return err
	}

	return nil
}

func (c *Client) readProtocolVersion() (major, minor int, err error) {
	line, err := readFull(c.r, 12)
	if err != nil {
		return 0, 0, fmt.Errorf("rfb: reading protocol version: %w", err)
	}
	n, err := fmt.Sscanf(string(line), "RFB %03d.%03d\n", &major, &minor)
	if err != nil || n != 2 {
		return 0, 0, protoErrf("unrecognised protocol version line %q", line)
	}
	return major, minor, nil
}

func (c *Client) negotiateSecurity(major, minor int, username, password string) (uint8, error) {
	if major == 3 && minor == 3 {
		// RFB 3.3: the server dictates the security type directly.
		secType, err := readUint32(c.r)
		if err != nil {
			return 0, err
		}
		if secType == secInvalid {
			reasonLen, err := readUint32(c.r)
			if err != nil {
				return 0, err
			}
			reason, err := readFull(c.r, int(reasonLen))
			if err != nil {
				return 0, err
			}
			return 0, &AuthError{Reason: string(reason)}
		}
		return uint8(secType), nil
	}

	// RFB 3.7/3.8: the server offers a list, the client picks one.
	numTypes, err := readUint8(c.r)
	if err != nil {
		return 0, err
	}
	if numTypes == 0 {
		reasonLen, err := readUint32(c.r)
		if err != nil {
			return 0, err
		}
		reason, err := readFull(c.r, int(reasonLen))
		if err != nil {
			return 0, err
		}
		return 0, &AuthError{Reason: string(reason)}
	}

	offered, err := readFull(c.r, int(numTypes))
	if err != nil {
		return 0, err
	}

	var chosen uint8
	for _, t := range offered {
		if t == secVNCAuth && password != "" {
			chosen = secVNCAuth
			break
		}
	}
	if chosen == 0 {
		// Servers that require a real user-account login (rather than a
		// shared VNC password) — chiefly macOS's built-in Screen Sharing
		// — offer only secARD, no secVNCAuth. See ard.go.
		for _, t := range offered {
			if t == secARD && (username != "" || password != "") {
				chosen = secARD
				break
			}
		}
	}
	if chosen == 0 {
		for _, t := range offered {
			if t == secNone {
				chosen = secNone
				break
			}
		}
	}
	if chosen == 0 {
		for _, t := range offered {
			if t == secVNCAuth {
				chosen = secVNCAuth
				break
			}
		}
	}
	if chosen == 0 {
		return 0, protoErrf("server only offers unsupported security types %v", offered)
	}

	if err := writeUint8(c.w, chosen); err != nil {
		return 0, err
	}
	return chosen, nil
}

func (c *Client) performVNCAuth(password string) error {
	challengeBytes, err := readFull(c.r, 16)
	if err != nil {
		return err
	}
	var challenge [16]byte
	copy(challenge[:], challengeBytes)

	response, err := vncAuthResponse(password, challenge)
	if err != nil {
		return err
	}
	_, err = c.w.Write(response[:])
	return err
}

func (c *Client) readServerInit() error {
	width, err := readUint16(c.r)
	if err != nil {
		return err
	}
	height, err := readUint16(c.r)
	if err != nil {
		return err
	}

	// We ignore the server's native pixel format entirely: we're about to
	// override it with SetPixelFormat below. Still have to read past the
	// 16 bytes on the wire.
	if _, err := readFull(c.r, 16); err != nil {
		return err
	}

	nameLen, err := readUint32(c.r)
	if err != nil {
		return err
	}
	nameBytes, err := readFull(c.r, int(nameLen))
	if err != nil {
		return err
	}

	c.width = int(width)
	c.height = int(height)
	c.name = string(nameBytes)
	return nil
}

func (c *Client) sendSetPixelFormat() error {
	buf := make([]byte, 20)
	buf[0] = msgSetPixelFormat
	pf := requestedPixelFormat
	buf[4] = pf.BitsPerPixel
	buf[5] = pf.Depth
	buf[6] = pf.BigEndian
	buf[7] = pf.TrueColor
	buf[8] = byte(pf.RedMax >> 8)
	buf[9] = byte(pf.RedMax)
	buf[10] = byte(pf.GreenMax >> 8)
	buf[11] = byte(pf.GreenMax)
	buf[12] = byte(pf.BlueMax >> 8)
	buf[13] = byte(pf.BlueMax)
	buf[14] = pf.RedShift
	buf[15] = pf.GreenShift
	buf[16] = pf.BlueShift
	_, err := c.w.Write(buf)
	return err
}

func (c *Client) sendSetEncodings() error {
	// Deliberately not advertising EncodingExtendedDesktopSize here: tested
	// live against a real TigerVNC/Xvnc server, advertising it made that
	// server only ever send the ExtendedDesktopSize + Cursor capability
	// rects and never any real pixel content, for the lifetime of the
	// connection — a server-side interop issue, not a stream-parsing bug
	// (rectangle counts and byte accounting checked out exactly). Plain
	// DesktopSize still gets us server-driven resize notifications; the
	// decoder for ExtendedDesktopSize (decodeExtendedDesktopSize) is left
	// in place and correct, it's just not negotiated. Multi-monitor/
	// layout-aware resize is already a roadmap item, not v0.1.0 scope.
	compressLevel, qualityLevel := qualityPresetLevels(c.quality)
	encodings := []int32{
		EncodingTight,
		EncodingZRLE,
		EncodingCopyRect,
		EncodingRaw,
		EncodingCursor,
		EncodingDesktopSize,
		encodingCompressLevel0 + int32(compressLevel),
		encodingQualityLevel0 + int32(qualityLevel),
	}

	buf := make([]byte, 4+4*len(encodings))
	buf[0] = msgSetEncodings
	buf[2] = byte(len(encodings) >> 8)
	buf[3] = byte(len(encodings))
	for i, enc := range encodings {
		off := 4 + i*4
		buf[off] = byte(uint32(enc) >> 24)
		buf[off+1] = byte(uint32(enc) >> 16)
		buf[off+2] = byte(uint32(enc) >> 8)
		buf[off+3] = byte(uint32(enc))
	}
	_, err := c.w.Write(buf)
	return err
}
