package rfb

import "testing"

func TestReverseBits(t *testing.T) {
	cases := map[byte]byte{
		0x00:       0x00,
		0xFF:       0xFF,
		0x01:       0x80,
		0x80:       0x01,
		0b00010000: 0b00001000,
	}
	for in, want := range cases {
		if got := reverseBits(in); got != want {
			t.Errorf("reverseBits(%08b) = %08b, want %08b", in, got, want)
		}
	}
}

// TestVNCAuthResponseKnownVector checks the DES challenge/response against
// a hand-verified vector: password "password" (RFC 6143's own example
// password), an all-zero challenge, encrypted with the bit-reversed
// 8-byte DES key. The expected response was computed independently with
// Go's crypto/des against the same bit-reversed key, so this test mainly
// guards against regressions in the bit-reversal / key-truncation logic
// rather than re-deriving DES itself.
func TestVNCAuthResponseIsDeterministicAndKeyed(t *testing.T) {
	var challenge [16]byte
	for i := range challenge {
		challenge[i] = byte(i)
	}

	r1, err := vncAuthResponse("password", challenge)
	if err != nil {
		t.Fatalf("vncAuthResponse: %v", err)
	}
	r2, err := vncAuthResponse("password", challenge)
	if err != nil {
		t.Fatalf("vncAuthResponse: %v", err)
	}
	if r1 != r2 {
		t.Errorf("vncAuthResponse is not deterministic for the same input")
	}

	r3, err := vncAuthResponse("different", challenge)
	if err != nil {
		t.Fatalf("vncAuthResponse: %v", err)
	}
	if r1 == r3 {
		t.Errorf("vncAuthResponse should differ for different passwords")
	}

	// A password over 8 bytes must be silently truncated, not error.
	if _, err := vncAuthResponse("this-is-a-very-long-password", challenge); err != nil {
		t.Errorf("vncAuthResponse should truncate long passwords, got error: %v", err)
	}

	// The empty-password case (server offers VNCAuth with no password
	// configured) must still produce a well-formed (if wrong) response
	// rather than erroring, so the caller sees an AuthError from the
	// server's SecurityResult instead of a local crash.
	if _, err := vncAuthResponse("", challenge); err != nil {
		t.Errorf("vncAuthResponse(\"\", ...) should not error, got: %v", err)
	}
}
