package rfb

import "crypto/des"

// vncAuthResponse implements the VNC Authentication scheme (RFC 6143
// §7.2.2): the password is used, bit-reversed byte-by-byte, as a DES key
// (truncated/zero-padded to 8 bytes) to encrypt the server's 16-byte
// challenge in two independent 8-byte ECB blocks.
func vncAuthResponse(password string, challenge [16]byte) ([16]byte, error) {
	var key [8]byte
	for i := 0; i < 8 && i < len(password); i++ {
		key[i] = reverseBits(password[i])
	}

	block, err := des.NewCipher(key[:])
	if err != nil {
		return [16]byte{}, err
	}

	var response [16]byte
	block.Encrypt(response[0:8], challenge[0:8])
	block.Encrypt(response[8:16], challenge[8:16])
	return response, nil
}

// reverseBits reverses the bits within a single byte. The VNC Authentication
// scheme does this to every byte of the password before using it as a DES
// key — a well-known quirk of the original RealVNC implementation that every
// client must replicate for interoperability.
func reverseBits(b byte) byte {
	var out byte
	for i := 0; i < 8; i++ {
		out <<= 1
		out |= b & 1
		b >>= 1
	}
	return out
}
