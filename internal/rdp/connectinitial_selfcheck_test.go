package rdp

import "testing"

// TestConnectInitialSelfConsistent parses the client's own generated
// Connect Initial BER structure the way a server would, verifying every
// declared length matches the actual bytes present — a bug here would
// explain a server misjudging how many bytes to expect for the GCC user
// data that Connect Initial carries.
func TestConnectInitialSelfConsistent(t *testing.T) {
	gccData := buildGCCConferenceCreateRequest(defaultWidth, defaultHeight)
	t.Logf("gccData: %d bytes", len(gccData))

	// Reconstruct exactly what sendConnectInitial builds, without a live
	// connection.
	var body []byte
	body = append(body, berTagOctetString, 1, 1)
	body = append(body, berTagOctetString, 1, 1)
	body = append(body, berTagBoolean, 1, 0xFF)
	body = append(body, domainParameters(34, 2, 0, 1, 0, 1, 0xFFFF, 2)...)
	body = append(body, domainParameters(1, 1, 1, 1, 0, 1, 0x420, 2)...)
	body = append(body, domainParameters(0xFFFF, 0xFFFF, 0xFFFF, 1, 0, 1, 0xFFFF, 2)...)

	// The userData TLV itself, built the same way berTLV would.
	userDataTLV := append([]byte{berTagOctetString}, berLength(len(gccData))...)
	userDataTLV = append(userDataTLV, gccData...)
	body = append(body, userDataTLV...)

	var pdu []byte
	pdu = append(pdu, berApplicationTag(101)...)
	pdu = append(pdu, berLength(len(body))...)
	pdu = append(pdu, body...)

	t.Logf("full Connect Initial PDU: %d bytes", len(pdu))

	// Now parse it back exactly as readConnectResponse-style code would,
	// using berDecoder, and verify every length is self-consistent.
	d := newBERDecoder(pdu)
	tag, err := d.readTag()
	if err != nil {
		t.Fatalf("readTag: %v", err)
	}
	if tag != 0x7F65 {
		t.Fatalf("expected Connect-Initial application tag 0x7F65, got 0x%x", tag)
	}
	outerLen, err := d.readLength()
	if err != nil {
		t.Fatalf("readLength: %v", err)
	}
	if outerLen != len(body) {
		t.Errorf("outer BER length %d != actual body length %d", outerLen, len(body))
	}
	if d.pos+outerLen != len(pdu) {
		t.Errorf("outer length %d starting at pos %d does not exactly reach end of PDU (len %d)", outerLen, d.pos, len(pdu))
	}

	// callingDomainSelector, calledDomainSelector, upwardFlag
	for i, name := range []string{"callingDomainSelector", "calledDomainSelector", "upwardFlag"} {
		_, val, err := d.readTLV()
		if err != nil {
			t.Fatalf("field %d (%s): %v", i, name, err)
		}
		t.Logf("%s: % x", name, val)
	}
	for _, name := range []string{"targetParameters", "minimumParameters", "maximumParameters"} {
		tag, val, err := d.readTLV()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if tag != berTagSequence {
			t.Errorf("%s: expected SEQUENCE tag 0x%x, got 0x%x", name, berTagSequence, tag)
		}
		t.Logf("%s: %d bytes", name, len(val))
	}
	tag, userData, err := d.readTLV()
	if err != nil {
		t.Fatalf("userData: %v", err)
	}
	if tag != berTagOctetString {
		t.Errorf("userData: expected OCTET STRING tag 0x%x, got 0x%x", berTagOctetString, tag)
	}
	if len(userData) != len(gccData) {
		t.Errorf("userData length %d != gccData length %d", len(userData), len(gccData))
	}
	if d.remaining() != 0 {
		t.Errorf("%d trailing bytes left unconsumed after parsing all fields", d.remaining())
	}
	t.Logf("userData (gccCCrq): %d bytes, fully self-consistent", len(userData))
}
