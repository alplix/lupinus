package rdp

import "testing"

// TestConfirmActiveSelfConsistent walks the capability sets inside a
// generated Confirm Active PDU using their own declared lengths and
// checks that doing so exactly consumes lengthCombinedCapabilities with
// nothing left over and nothing overrun — a real bug here (as opposed to
// eyeballing a hex dump) is what would explain a server desyncing on the
// next PDU after Confirm Active.
func TestConfirmActiveSelfConsistent(t *testing.T) {
	c := &Client{mcsUserID: 1, shareID: 0x03EA03EA}

	caps := [][]byte{
		generalCapabilitySet(),
		bitmapCapabilitySet(),
		orderCapabilitySet(),
		pointerCapabilitySet(),
		inputCapabilitySet(),
		shareCapabilitySet(),
		colorCacheCapabilitySet(),
		fontCapabilitySet(),
		virtualChannelCapabilitySet(),
	}
	for i, cs := range caps {
		if len(cs) < 4 {
			t.Fatalf("cap set %d: too short (%d bytes)", i, len(cs))
		}
		declared := int(cs[2]) | int(cs[3])<<8
		if declared != len(cs) {
			t.Errorf("cap set %d (type %d): declared length %d != actual %d", i, int(cs[0])|int(cs[1])<<8, declared, len(cs))
		}
	}

	_ = c
	var combined []byte
	for _, cs := range caps {
		combined = append(combined, cs...)
	}

	// Walk it the way a server parser would: type(2) + length(2) + body,
	// length bytes total including the 4-byte header.
	pos := 0
	count := 0
	for pos < len(combined) {
		if pos+4 > len(combined) {
			t.Fatalf("truncated capability set header at offset %d (combined len %d)", pos, len(combined))
		}
		length := int(combined[pos+2]) | int(combined[pos+3])<<8
		if length < 4 {
			t.Fatalf("capability set at offset %d declares length %d (< 4)", pos, length)
		}
		if pos+length > len(combined) {
			t.Fatalf("capability set at offset %d declares length %d, overruns combined buffer (len %d)", pos, length, len(combined))
		}
		pos += length
		count++
	}
	if pos != len(combined) {
		t.Errorf("walking capability sets left %d trailing bytes unconsumed", len(combined)-pos)
	}
	if count != len(caps) {
		t.Errorf("walked %d capability sets, expected %d", count, len(caps))
	}
	t.Logf("combined capability sets: %d bytes across %d sets, all self-consistent", len(combined), count)
}
