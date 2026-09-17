package rfb

import (
	"bufio"
	"crypto/aes"
	"crypto/md5"
	"crypto/rand"
	"math/big"
	"net"
	"testing"
	"time"
)

// ardTestPrimeHex is RFC 2409's Oakley Group 1: a real, well-known 768-bit
// MODP prime (generator 2), used here purely as a valid Diffie-Hellman
// modulus so this test exercises real modular exponentiation at a
// realistic size — not because ARD itself mandates this specific prime
// (the server picks its own at connect time; this client accepts whatever
// generator/modulus it's sent, see performARDAuth).
const ardTestPrimeHex = "FFFFFFFFFFFFFFFFC90FDAA22168C234C4C6628B80DC1CD1" +
	"29024E088A67CC74020BBEA63B139B22514A08798E3404DD" +
	"EF9519B3CD3A431B302B0A6DF25F14374FE1356D6D51C245" +
	"E485B576625E7EC6F44C42E9A63A3620FFFFFFFFFFFFFFFF"

// TestARDAuthAgainstMockServer exercises performARDAuth end-to-end against
// an independent mock server implementation (its own DH keypair, its own
// AES-128-ECB decryption) over a real net.Conn — no real macOS box is
// available to test against, so this is the strongest verification
// available: it confirms the client's wire framing, field packing and
// crypto genuinely interoperate with a from-scratch second implementation
// of the same protocol, not just with itself.
func TestARDAuthAgainstMockServer(t *testing.T) {
	const username = "alp"
	const password = "hunter2"

	prime, ok := new(big.Int).SetString(ardTestPrimeHex, 16)
	if !ok {
		t.Fatal("bad test prime")
	}
	keylen := (prime.BitLen() + 7) / 8
	generator := big.NewInt(2)

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	c := &Client{
		r: bufio.NewReader(clientConn),
		w: clientConn,
	}

	clientErrCh := make(chan error, 1)
	go func() {
		clientErrCh <- c.performARDAuth(username, password)
	}()

	serverErrCh := make(chan error, 1)
	go func() {
		serverErrCh <- mockARDServer(serverConn, generator, prime, keylen, username, password)
	}()

	select {
	case err := <-clientErrCh:
		if err != nil {
			t.Fatalf("performARDAuth: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for client")
	}
	select {
	case err := <-serverErrCh:
		if err != nil {
			t.Fatalf("mock server: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for mock server")
	}
}

func mockARDServer(conn net.Conn, generator, prime *big.Int, keylen int, wantUsername, wantPassword string) error {
	genBytes := leftPad(generator.Bytes(), 2)
	if _, err := conn.Write(genBytes); err != nil {
		return err
	}
	if _, err := conn.Write([]byte{byte(keylen >> 8), byte(keylen)}); err != nil {
		return err
	}
	if _, err := conn.Write(leftPad(prime.Bytes(), keylen)); err != nil {
		return err
	}

	privMax := new(big.Int).Sub(prime, big.NewInt(3))
	privRand, err := rand.Int(rand.Reader, privMax)
	if err != nil {
		return err
	}
	priv := new(big.Int).Add(privRand, big.NewInt(2))
	pub := new(big.Int).Exp(generator, priv, prime)
	if _, err := conn.Write(leftPad(pub.Bytes(), keylen)); err != nil {
		return err
	}

	ciphertext, err := readFull(conn, 128)
	if err != nil {
		return err
	}
	clientPubBytes, err := readFull(conn, keylen)
	if err != nil {
		return err
	}
	clientPub := new(big.Int).SetBytes(clientPubBytes)

	shared := new(big.Int).Exp(clientPub, priv, prime)
	key := md5.Sum(leftPad(shared.Bytes(), keylen))

	block, err := aes.NewCipher(key[:])
	if err != nil {
		return err
	}
	var plaintext [128]byte
	for i := 0; i < len(ciphertext); i += aes.BlockSize {
		block.Decrypt(plaintext[i:i+aes.BlockSize], ciphertext[i:i+aes.BlockSize])
	}

	gotUsername := cStringField(plaintext[0:64])
	gotPassword := cStringField(plaintext[64:128])
	if gotUsername != wantUsername {
		return protoErrf("ard mock server: decrypted username %q, want %q", gotUsername, wantUsername)
	}
	if gotPassword != wantPassword {
		return protoErrf("ard mock server: decrypted password %q, want %q", gotPassword, wantPassword)
	}

	// Deliberately not writing a SecurityResult here: that 4-byte message
	// is read by handshake.go after performARDAuth returns, not by
	// performARDAuth itself, and this test calls it directly — writing
	// one nobody reads would just block forever on net.Pipe's
	// synchronous semantics.
	return nil
}

// cStringField reads a NUL-terminated string out of a fixed-size field —
// the reverse of packARDField.
func cStringField(field []byte) string {
	for i, b := range field {
		if b == 0 {
			return string(field[:i])
		}
	}
	return string(field)
}
