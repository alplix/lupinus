package rfb

import (
	"crypto/aes"
	"crypto/md5"
	"crypto/rand"
	"math/big"
)

// performARDAuth implements RFB security type 30, "Apple Remote Desktop"
// (Apple's registered extension, used by macOS's built-in Screen Sharing
// server whenever it's configured for real user-account login rather than
// a shared VNC password — see the secARD doc comment in types.go). Ported
// against LibVNCServer's HandleARDAuth (src/libvncclient/rfbclient.c) and
// its OpenSSL crypto backend (src/common/crypto_openssl.c), used as the
// reference because Apple has never published this protocol itself.
//
// The exchange: classic (non-ephemeral, no host authentication) Diffie-
// Hellman key agreement over a server-supplied generator/prime, MD5 of the
// shared secret as an AES-128 key, then a 128-byte { username[64],
// password[64] } block — each field NUL-terminated, the rest of each half
// filled with random bytes so the ciphertext doesn't leak field lengths —
// encrypted with that key in ECB mode (Go's stdlib has no ECB cipher.Mode,
// since it's a bad default for general use, but the wire format demands it
// here: encrypt each of the 8 16-byte blocks independently).
func (c *Client) performARDAuth(username, password string) error {
	genBytes, err := readFull(c.r, 2)
	if err != nil {
		return err
	}
	lenBytes, err := readFull(c.r, 2)
	if err != nil {
		return err
	}
	keylen := int(lenBytes[0])<<8 | int(lenBytes[1])

	modBytes, err := readFull(c.r, keylen)
	if err != nil {
		return err
	}
	peerPubBytes, err := readFull(c.r, keylen)
	if err != nil {
		return err
	}

	generator := new(big.Int).SetBytes(genBytes)
	prime := new(big.Int).SetBytes(modBytes)
	peerPub := new(big.Int).SetBytes(peerPubBytes)

	// Random private exponent in [2, prime-2], and the corresponding
	// public value generator^priv mod prime.
	privMax := new(big.Int).Sub(prime, big.NewInt(3))
	if privMax.Sign() <= 0 {
		return protoErrf("ard: server's DH prime is too small")
	}
	privRand, err := rand.Int(rand.Reader, privMax)
	if err != nil {
		return err
	}
	priv := new(big.Int).Add(privRand, big.NewInt(2))
	pub := new(big.Int).Exp(generator, priv, prime)
	shared := new(big.Int).Exp(peerPub, priv, prime)

	sharedKey := md5.Sum(leftPad(shared.Bytes(), keylen))

	var userpass [128]byte
	if _, err := rand.Read(userpass[:]); err != nil {
		return err
	}
	packARDField(userpass[0:64], username)
	packARDField(userpass[64:128], password)

	block, err := aes.NewCipher(sharedKey[:])
	if err != nil {
		return err
	}
	var ciphertext [128]byte
	for i := 0; i < len(userpass); i += aes.BlockSize {
		block.Encrypt(ciphertext[i:i+aes.BlockSize], userpass[i:i+aes.BlockSize])
	}

	if _, err := c.w.Write(ciphertext[:]); err != nil {
		return err
	}
	_, err = c.w.Write(leftPad(pub.Bytes(), keylen))
	return err
}

// packARDField writes s (including its NUL terminator) into the start of
// field, which must already be filled with random padding bytes — matching
// LibVNCServer's HandleARDAuth, which relies on the terminator (not
// zero-padding) to mark the field's real end. s is silently truncated,
// terminator included, if it doesn't fit.
func packARDField(field []byte, s string) {
	b := append([]byte(s), 0)
	if len(b) > len(field) {
		b = b[:len(field)]
		b[len(b)-1] = 0
	}
	copy(field, b)
}

// leftPad returns b left-padded with zero bytes to exactly n bytes —
// big.Int.Bytes() strips leading zeros, but the wire format (fixed-width
// modulus-sized fields) needs them back.
func leftPad(b []byte, n int) []byte {
	if len(b) >= n {
		return b[len(b)-n:]
	}
	out := make([]byte, n)
	copy(out[n-len(b):], b)
	return out
}
