package rfb

// requestUpdate sends a FramebufferUpdateRequest. incremental=false asks
// for the full rectangle regardless of what's changed (used once at
// startup); incremental=true (used for every request thereafter) asks the
// server to only send what's changed since the last update, and to block
// until something has.
func (c *Client) requestUpdate(incremental bool, x, y, w, h int) error {
	buf := make([]byte, 10)
	buf[0] = msgFramebufferUpdateRequest
	if incremental {
		buf[1] = 1
	}
	buf[2] = byte(x >> 8)
	buf[3] = byte(x)
	buf[4] = byte(y >> 8)
	buf[5] = byte(y)
	buf[6] = byte(w >> 8)
	buf[7] = byte(w)
	buf[8] = byte(h >> 8)
	buf[9] = byte(h)

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err := c.w.Write(buf)
	return err
}

// SendPointerEvent reports the current pointer position and button mask
// (bit 0 = left, bit 1 = middle, bit 2 = right, bits 3/4 = wheel up/down).
func (c *Client) SendPointerEvent(x, y int, buttonMask uint8) error {
	buf := make([]byte, 6)
	buf[0] = msgPointerEvent
	buf[1] = buttonMask
	buf[2] = byte(x >> 8)
	buf[3] = byte(x)
	buf[4] = byte(y >> 8)
	buf[5] = byte(y)

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err := c.w.Write(buf)
	return err
}

// SendKeyEvent reports a key press or release, identified by X11 keysym
// (see the frontend's keysym.js for how browser KeyboardEvents are mapped).
func (c *Client) SendKeyEvent(keysym uint32, down bool) error {
	buf := make([]byte, 8)
	buf[0] = msgKeyEvent
	if down {
		buf[1] = 1
	}
	buf[4] = byte(keysym >> 24)
	buf[5] = byte(keysym >> 16)
	buf[6] = byte(keysym >> 8)
	buf[7] = byte(keysym)

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err := c.w.Write(buf)
	return err
}

// SendClientCutText pushes local clipboard text to the server.
func (c *Client) SendClientCutText(text string) error {
	body := []byte(text)
	buf := make([]byte, 8+len(body))
	buf[0] = msgClientCutText
	buf[4] = byte(len(body) >> 24)
	buf[5] = byte(len(body) >> 16)
	buf[6] = byte(len(body) >> 8)
	buf[7] = byte(len(body))
	copy(buf[8:], body)

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err := c.w.Write(buf)
	return err
}

func (c *Client) skipSetColourMapEntries() error {
	// padding(1) + first-colour(2) + number-of-colours(2), then 3×u16 per
	// colour. Lupinus always negotiates true-colour, so servers shouldn't
	// send this, but we must still parse past it defensively.
	if _, err := readFull(c.r, 1); err != nil {
		return err
	}
	if _, err := readUint16(c.r); err != nil {
		return err
	}
	n, err := readUint16(c.r)
	if err != nil {
		return err
	}
	_, err = readFull(c.r, int(n)*6)
	return err
}

func (c *Client) readServerCutText() (string, error) {
	if _, err := readFull(c.r, 3); err != nil { // padding
		return "", err
	}
	length, err := readUint32(c.r)
	if err != nil {
		return "", err
	}
	body, err := readFull(c.r, int(length))
	if err != nil {
		return "", err
	}
	return string(body), nil
}
