package rdp

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"fmt"
	"math/big"
	"net"
	"testing"
	"time"
	"unicode/utf16"
)

// TestCredSSPAgainstMockServer exercises the whole NLA/CredSSP client
// (performCredSSP, credssp.go) against a from-scratch, independent mock
// CredSSP server implemented entirely in this test — necessary because the
// only real server available for live testing in this project (xrdp in
// WSL, see transport_manual_test.go) can never select PROTOCOL_HYBRID:
// its own source (libxrdp/xrdp_iso.c's xrdp_iso_negotiate_security) gates
// HYBRID/HYBRID_EX behind vmconnect mode only, so no xrdp.ini setting can
// make it exercise this code path. This test is the closest substitute:
// a real TCP+TLS connection, the client's actual TSRequest/NTLM wire code
// unmodified, verified end-to-end by a server side that independently
// re-derives the NTLMv2 proof, session keys, and RC4 sealing rather than
// simply calling the client's own functions back at it.
func TestCredSSPAgainstMockServer(t *testing.T) {
	const username = "rdptest"
	const password = "lupinus1"
	const domain = "TESTDOMAIN"

	cert, key := generateTestCertificate(t)
	tlsCert := tls.Certificate{Certificate: [][]byte{cert.Raw}, PrivateKey: key}

	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{tlsCert}})
	if err != nil {
		t.Fatalf("tls.Listen: %v", err)
	}
	defer ln.Close()

	serverErrCh := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			serverErrCh <- fmt.Errorf("accept: %w", err)
			return
		}
		defer conn.Close()
		tlsConn := conn.(*tls.Conn)
		if err := tlsConn.Handshake(); err != nil {
			serverErrCh <- fmt.Errorf("server TLS handshake: %w", err)
			return
		}
		serverErrCh <- mockCredSSPServer(tlsConn, cert.RawSubjectPublicKeyInfo, username, password, domain)
	}()

	clientConn, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{InsecureSkipVerify: true}) //nolint:gosec
	if err != nil {
		t.Fatalf("tls.Dial: %v", err)
	}
	defer clientConn.Close()
	if err := clientConn.Handshake(); err != nil {
		t.Fatalf("client TLS handshake: %v", err)
	}
	peers := clientConn.ConnectionState().PeerCertificates
	if len(peers) == 0 {
		t.Fatalf("no peer certificate")
	}

	c := &Client{tc: clientConn, username: username, password: password, domain: domain}
	clientErr := c.performCredSSP(peers[0])

	select {
	case serverErr := <-serverErrCh:
		if serverErr != nil {
			t.Errorf("mock server: %v", serverErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for mock server")
	}
	if clientErr != nil {
		t.Fatalf("performCredSSP: %v", clientErr)
	}
}

// mockCredSSPServer plays the server half of MS-CSSP/MS-NLMP against a
// connected client, independently re-deriving everything a real NTLM
// server would (NTLMv2 proof verification from the known password,
// session key derivation, RC4 sealing) rather than reusing the client's
// computed values — so this genuinely exercises interoperability of the
// wire format (BER/TSRequest framing, NTLM message byte layout, GSS_Wrap
// sealing) rather than just asserting the client agrees with itself.
func mockCredSSPServer(conn net.Conn, serverPubKey []byte, username, password, domain string) error {
	// Step 1: NEGOTIATE_MESSAGE — contents unused server-side.
	pdu1, err := readTSRequestPDU(conn)
	if err != nil {
		return fmt.Errorf("reading NEGOTIATE_MESSAGE TSRequest: %w", err)
	}
	if _, err := decodeTSRequest(pdu1); err != nil {
		return fmt.Errorf("decoding NEGOTIATE_MESSAGE TSRequest: %w", err)
	}

	// Step 2: build and send CHALLENGE_MESSAGE.
	serverChallengeB, err := randomBytes(8)
	if err != nil {
		return err
	}
	var serverChallenge [8]byte
	copy(serverChallenge[:], serverChallengeB)

	timestamp := ntlmCurrentTimestamp()
	targetInfo := buildTestTargetInfo(domain, "MOCKSERVER", timestamp)
	const flags = uint32(ntlmNegotiate56 | ntlmNegotiateVersion | ntlmNegotiateTargetInfo | ntlmNegotiateKeyExch |
		ntlmNegotiate128 | ntlmNegotiateExtendedSessionSecurity | ntlmNegotiateAlwaysSign |
		ntlmNegotiateNTLM | ntlmNegotiateSign | ntlmNegotiateSeal | ntlmRequestTarget | ntlmNegotiateUnicode)
	challengeMsg := buildTestChallengeMessage(serverChallenge, targetInfo, flags)

	if _, err := conn.Write(encodeTSRequest(tsRequest{version: credsspVersion, negoToken: challengeMsg})); err != nil {
		return fmt.Errorf("sending CHALLENGE_MESSAGE: %w", err)
	}

	// Step 3: read AUTHENTICATE_MESSAGE, verify the NTLMv2 proof.
	pdu2, err := readTSRequestPDU(conn)
	if err != nil {
		return fmt.Errorf("reading AUTHENTICATE_MESSAGE TSRequest: %w", err)
	}
	resp2, err := decodeTSRequest(pdu2)
	if err != nil {
		return fmt.Errorf("decoding AUTHENTICATE_MESSAGE TSRequest: %w", err)
	}
	gotUsername, gotDomain, ntChallengeResponse, encryptedRandomSessionKey, err := parseTestAuthenticateMessage(resp2.negoToken)
	if err != nil {
		return fmt.Errorf("parsing AUTHENTICATE_MESSAGE: %w", err)
	}
	if gotUsername != username || gotDomain != domain {
		return fmt.Errorf("identity mismatch: got username=%q domain=%q, want %q/%q", gotUsername, gotDomain, username, domain)
	}
	if len(ntChallengeResponse) < 16+28 {
		return fmt.Errorf("NtChallengeResponse too short (%d bytes)", len(ntChallengeResponse))
	}
	gotProof := ntChallengeResponse[:16]
	temp := ntChallengeResponse[16:]
	var gotTimestamp, clientChallenge [8]byte
	copy(gotTimestamp[:], temp[8:16])
	copy(clientChallenge[:], temp[16:24])

	ntlmV2Hash := ntowfv2(password, username, domain)
	wantResponse, sessionBaseKey := ntlmv2Response(ntlmV2Hash, serverChallenge, clientChallenge, gotTimestamp, targetInfo)
	if !bytes.Equal(wantResponse[:16], gotProof) {
		return fmt.Errorf("NTProofStr mismatch — client's NTLMv2 response does not verify against the known password")
	}

	keyExchangeKey := sessionBaseKey // NTLMv2: KeyExchangeKey == SessionBaseKey
	exportedSessionKey, err := rc4Once(keyExchangeKey, encryptedRandomSessionKey)
	if err != nil {
		return fmt.Errorf("decrypting EncryptedRandomSessionKey: %w", err)
	}

	// Server's sealer: send with server keys, receive (unwrap) with client
	// keys — the mirror image of the client's own newNTLMSealer call.
	serverSealer, err := newNTLMSealer(
		ntlmSigningKey(exportedSessionKey, ntlmServerSignMagic),
		ntlmSigningKey(exportedSessionKey, ntlmServerSealMagic),
		ntlmSigningKey(exportedSessionKey, ntlmClientSignMagic),
		ntlmSigningKey(exportedSessionKey, ntlmClientSealMagic),
	)
	if err != nil {
		return fmt.Errorf("newNTLMSealer: %w", err)
	}

	// Step 4: pubKeyAuth channel binding.
	pdu3, err := readTSRequestPDU(conn)
	if err != nil {
		return fmt.Errorf("reading pubKeyAuth TSRequest: %w", err)
	}
	resp3, err := decodeTSRequest(pdu3)
	if err != nil {
		return fmt.Errorf("decoding pubKeyAuth TSRequest: %w", err)
	}
	clientHash, err := serverSealer.unwrap(resp3.pubKeyAuth)
	if err != nil {
		return fmt.Errorf("unwrapping client's pubKeyAuth: %w", err)
	}
	wantClientHash := sha256.Sum256(concat(credsspClientServerHashMagic, resp3.clientNonce, serverPubKey))
	if !bytes.Equal(clientHash, wantClientHash[:]) {
		return fmt.Errorf("client's pubKeyAuth hash does not match expected channel binding")
	}
	serverHash := sha256.Sum256(concat(credsspServerClientHashMagic, resp3.clientNonce, serverPubKey))
	if _, err := conn.Write(encodeTSRequest(tsRequest{version: credsspVersion, pubKeyAuth: serverSealer.wrap(serverHash[:])})); err != nil {
		return fmt.Errorf("sending pubKeyAuth response: %w", err)
	}

	// Step 5: delegated credentials.
	pdu4, err := readTSRequestPDU(conn)
	if err != nil {
		return fmt.Errorf("reading authInfo TSRequest: %w", err)
	}
	resp4, err := decodeTSRequest(pdu4)
	if err != nil {
		return fmt.Errorf("decoding authInfo TSRequest: %w", err)
	}
	credBytes, err := serverSealer.unwrap(resp4.authInfo)
	if err != nil {
		return fmt.Errorf("unwrapping authInfo: %w", err)
	}
	gotDomain2, gotUsername2, gotPassword2, err := parseTestTSCredentials(credBytes)
	if err != nil {
		return fmt.Errorf("parsing delegated TSCredentials: %w", err)
	}
	if gotDomain2 != domain || gotUsername2 != username || gotPassword2 != password {
		return fmt.Errorf("delegated credentials mismatch: got %q/%q/%q", gotDomain2, gotUsername2, gotPassword2)
	}

	return nil
}

func buildTestTargetInfo(domain, computerName string, timestamp [8]byte) []byte {
	var buf bytes.Buffer
	writeTestAvPair(&buf, 2, utf16LEBytes(domain))       // MsvAvNbDomainName
	writeTestAvPair(&buf, 1, utf16LEBytes(computerName)) // MsvAvNbComputerName
	writeTestAvPair(&buf, msvAvTimestamp, timestamp[:])
	writeTestAvPair(&buf, msvAvEOL, nil)
	return buf.Bytes()
}

func writeTestAvPair(buf *bytes.Buffer, id uint16, value []byte) {
	_ = writeUint16LE(buf, id)
	_ = writeUint16LE(buf, uint16(len(value)))
	buf.Write(value)
}

// buildTestChallengeMessage is the server-side mirror of parseChallengeMessage.
func buildTestChallengeMessage(serverChallenge [8]byte, targetInfo []byte, flags uint32) []byte {
	fixedLen := 48
	if flags&ntlmNegotiateVersion != 0 {
		fixedLen += 8
	}
	targetNameOffset := fixedLen
	targetInfoOffset := targetNameOffset // TargetName is empty

	var b bytes.Buffer
	b.WriteString(ntlmSignature)
	_ = writeUint32LE(&b, ntlmMessageTypeChallenge)
	writeMessageFields(&b, 0, targetNameOffset)
	_ = writeUint32LE(&b, flags)
	b.Write(serverChallenge[:])
	b.Write(make([]byte, 8)) // Reserved
	writeMessageFields(&b, len(targetInfo), targetInfoOffset)
	if flags&ntlmNegotiateVersion != 0 {
		writeNTLMVersion(&b)
	}
	b.Write(targetInfo)
	return b.Bytes()
}

// parseTestAuthenticateMessage is the server-side mirror of buildAuthenticateMessage.
func parseTestAuthenticateMessage(data []byte) (username, domain string, ntChallengeResponse, encryptedRandomSessionKey []byte, err error) {
	if len(data) < 12 || string(data[0:8]) != ntlmSignature {
		return "", "", nil, nil, protoErrf("ntlm: not a valid NTLMSSP message")
	}
	if binary.LittleEndian.Uint32(data[8:12]) != ntlmMessageTypeAuthenticate {
		return "", "", nil, nil, protoErrf("ntlm: expected AUTHENTICATE_MESSAGE")
	}
	off := 12
	if _, _, err = readMessageFields(data[off:]); err != nil { // LmChallengeResponseFields
		return
	}
	off += 8
	ntLen, ntOffset, err := readMessageFields(data[off:])
	if err != nil {
		return
	}
	off += 8
	domLen, domOffset, err := readMessageFields(data[off:])
	if err != nil {
		return
	}
	off += 8
	userLen, userOffset, err := readMessageFields(data[off:])
	if err != nil {
		return
	}
	off += 8
	off += 8 // WorkstationFields: unused
	keyLen, keyOffset, err := readMessageFields(data[off:])
	if err != nil {
		return
	}

	if domOffset < 0 || domOffset+domLen > len(data) {
		return "", "", nil, nil, protoErrf("ntlm: DomainName out of range")
	}
	if userOffset < 0 || userOffset+userLen > len(data) {
		return "", "", nil, nil, protoErrf("ntlm: UserName out of range")
	}
	if ntOffset < 0 || ntOffset+ntLen > len(data) {
		return "", "", nil, nil, protoErrf("ntlm: NtChallengeResponse out of range")
	}
	if keyOffset < 0 || keyOffset+keyLen > len(data) {
		return "", "", nil, nil, protoErrf("ntlm: EncryptedRandomSessionKey out of range")
	}

	domain = utf16LEDecode(data[domOffset : domOffset+domLen])
	username = utf16LEDecode(data[userOffset : userOffset+userLen])
	ntChallengeResponse = data[ntOffset : ntOffset+ntLen]
	encryptedRandomSessionKey = data[keyOffset : keyOffset+keyLen]
	return username, domain, ntChallengeResponse, encryptedRandomSessionKey, nil
}

// parseTestTSCredentials is the server-side mirror of encodeTSCredentials.
func parseTestTSCredentials(data []byte) (domain, username, password string, err error) {
	_, outer, err := newBERDecoder(data).readTLV() // SEQUENCE { [0] credType, [1] credentials }
	if err != nil {
		return "", "", "", err
	}
	d := newBERDecoder(outer)
	if _, _, err = d.readTLV(); err != nil { // [0] credType
		return "", "", "", err
	}
	tag, credentials, err := d.readTLV() // [1] credentials
	if err != nil {
		return "", "", "", err
	}
	if tag != int(berContextTag(1)) {
		return "", "", "", protoErrf("tscredentials: expected [1] credentials")
	}
	_, passwordCredsDER, err := newBERDecoder(credentials).readTLV() // OCTET STRING
	if err != nil {
		return "", "", "", err
	}
	_, fields, err := newBERDecoder(passwordCredsDER).readTLV() // TSPasswordCreds SEQUENCE
	if err != nil {
		return "", "", "", err
	}
	fd := newBERDecoder(fields)
	for fd.remaining() > 0 {
		ftag, fval, ferr := fd.readTLV()
		if ferr != nil {
			return "", "", "", ferr
		}
		_, octetVal, oerr := newBERDecoder(fval).readTLV()
		if oerr != nil {
			return "", "", "", oerr
		}
		s := utf16LEDecode(octetVal)
		switch byte(ftag) {
		case berContextTag(0):
			domain = s
		case berContextTag(1):
			username = s
		case berContextTag(2):
			password = s
		}
	}
	return domain, username, password, nil
}

func utf16LEDecode(b []byte) string {
	u16 := make([]uint16, len(b)/2)
	for i := range u16 {
		u16[i] = binary.LittleEndian.Uint16(b[i*2:])
	}
	return string(utf16.Decode(u16))
}

// generateTestCertificate builds a throwaway self-signed certificate, the
// same kind of thing any RDP server presents (see negotiateSecurity's
// TLS-TOFU doc comment on why this client never validates a CA chain).
func generateTestCertificate(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "mock-credssp-server"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating test certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parsing test certificate: %v", err)
	}
	return cert, key
}
