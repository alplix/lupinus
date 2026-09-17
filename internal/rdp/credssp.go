package rdp

import (
	"bytes"
	"crypto/rc4"
	"crypto/sha256"
	"crypto/x509"
	"fmt"
	"io"
)

// CredSSP / NLA (MS-CSSP), from scratch — authenticates before the RDP
// protocol itself begins, carrying NTLM (ntlm.go) tokens inside a small
// BER-encoded TSRequest exchanged directly over the TLS connection
// negotiateSecurity already established (no TPKT/X.224 framing here; a
// TSRequest is self-describing DER, read by parsing its own SEQUENCE
// length). Ported against FreeRDP's libfreerdp/core/nla.c (the 2.x
// branch, which hand-rolls the BER — newer FreeRDP delegates to a generic
// ASN.1 library that obscures the exact bytes, unhelpful as a reference
// for a from-scratch implementation).
//
// Scope: NTLMv2 password authentication only (no Kerberos, no smart
// card), CredSSP protocol version 6 (the modern SHA-256 public-key-hash
// channel binding — the older "echo the whole key back" version predates
// the CredSSP encryption-oracle fix, CVE-2018-0886, and current
// Windows/xrdp only speak v5+ by default anyway).

const credsspVersion = 6

// CredSSP Client-To-Server Binding Hash\0 / Server-To-Client, verbatim
// including the trailing NUL FreeRDP's sizeof(char[]) includes.
var (
	credsspClientServerHashMagic = []byte("CredSSP Client-To-Server Binding Hash\x00")
	credsspServerClientHashMagic = []byte("CredSSP Server-To-Client Binding Hash\x00")
)

func berContextTag(n int) byte { return 0xA0 | byte(n) }

// tsRequest is the subset of TSRequest (MS-CSSP 2.2.1) this client ever
// needs to send or receive: version, at most one negoToken, and the
// optional authInfo/pubKeyAuth/clientNonce octet strings. errorCode is
// parsed (to surface a real server-reported failure reason) but never
// sent.
type tsRequest struct {
	version     uint32
	negoToken   []byte
	authInfo    []byte
	pubKeyAuth  []byte
	clientNonce []byte
	haveError   bool
	errorCode   uint32
}

func encodeTSRequest(req tsRequest) []byte {
	var body bytes.Buffer

	// [0] version (INTEGER) — EXPLICIT tagging: the context tag wraps a
	// nested INTEGER TLV, it isn't the integer's bytes directly.
	var versionInt bytes.Buffer
	berTLV(&versionInt, berTagInteger, berInteger(req.version))
	berTLV(&body, berContextTag(0), versionInt.Bytes())

	// [1] negoTokens (SEQUENCE OF SEQUENCE { [0] OCTET STRING })
	if len(req.negoToken) > 0 {
		var negoTokenOctet bytes.Buffer
		berTLV(&negoTokenOctet, berTagOctetString, req.negoToken)
		var item bytes.Buffer
		berTLV(&item, berContextTag(0), negoTokenOctet.Bytes())
		var itemSeq bytes.Buffer
		berTLV(&itemSeq, berTagSequence, item.Bytes())
		var negoTokens bytes.Buffer
		berTLV(&negoTokens, berTagSequence, itemSeq.Bytes())
		berTLV(&body, berContextTag(1), negoTokens.Bytes())
	}

	// [2] authInfo (OCTET STRING)
	if len(req.authInfo) > 0 {
		var inner bytes.Buffer
		berTLV(&inner, berTagOctetString, req.authInfo)
		berTLV(&body, berContextTag(2), inner.Bytes())
	}

	// [3] pubKeyAuth (OCTET STRING)
	if len(req.pubKeyAuth) > 0 {
		var inner bytes.Buffer
		berTLV(&inner, berTagOctetString, req.pubKeyAuth)
		berTLV(&body, berContextTag(3), inner.Bytes())
	}

	// [5] clientNonce (OCTET STRING) — sent on every client message once
	// generated, matching FreeRDP's nla_send (not just the first).
	if len(req.clientNonce) > 0 {
		var inner bytes.Buffer
		berTLV(&inner, berTagOctetString, req.clientNonce)
		berTLV(&body, berContextTag(5), inner.Bytes())
	}

	var out bytes.Buffer
	berTLV(&out, berTagSequence, body.Bytes())
	return out.Bytes()
}

func decodeTSRequest(data []byte) (tsRequest, error) {
	var req tsRequest
	d := newBERDecoder(data)

	tag, err := d.readTag()
	if err != nil {
		return req, err
	}
	if tag != int(berTagSequence) {
		return req, protoErrf("credssp: expected TSRequest SEQUENCE, got tag 0x%x", tag)
	}
	length, err := d.readLength()
	if err != nil {
		return req, err
	}
	if d.remaining() < length {
		return req, protoErrf("credssp: TSRequest length exceeds available data")
	}
	inner := newBERDecoder(d.buf[d.pos : d.pos+length])

	// [0] version
	tag, val, err := inner.readTLV()
	if err != nil {
		return req, err
	}
	if tag != int(berContextTag(0)) {
		return req, protoErrf("credssp: expected TSRequest [0] version")
	}
	vtag, vval, err := newBERDecoder(val).readTLV()
	if err != nil || vtag != int(berTagInteger) {
		return req, protoErrf("credssp: malformed TSRequest version")
	}
	req.version = berDecodeInteger(vval)

	for inner.remaining() > 0 {
		tag, val, err := inner.readTLV()
		if err != nil {
			return req, err
		}
		switch byte(tag) {
		case berContextTag(1): // negoTokens
			nt, err := decodeNegoTokens(val)
			if err != nil {
				return req, err
			}
			req.negoToken = nt
		case berContextTag(2): // authInfo
			_, octet, err := newBERDecoder(val).readTLV()
			if err != nil {
				return req, err
			}
			req.authInfo = octet
		case berContextTag(3): // pubKeyAuth
			_, octet, err := newBERDecoder(val).readTLV()
			if err != nil {
				return req, err
			}
			req.pubKeyAuth = octet
		case berContextTag(4): // errorCode
			_, ival, err := newBERDecoder(val).readTLV()
			if err != nil {
				return req, err
			}
			req.haveError = true
			req.errorCode = berDecodeInteger(ival)
		case berContextTag(5): // clientNonce
			_, octet, err := newBERDecoder(val).readTLV()
			if err != nil {
				return req, err
			}
			req.clientNonce = octet
		}
	}
	return req, nil
}

// decodeNegoTokens parses SEQUENCE OF SEQUENCE { [0] OCTET STRING } and
// returns the first (and, for this client's purposes, only) negoToken.
func decodeNegoTokens(data []byte) ([]byte, error) {
	_, seqOfItems, err := newBERDecoder(data).readTLV() // SEQUENCE OF NegoDataItem
	if err != nil {
		return nil, err
	}
	_, item, err := newBERDecoder(seqOfItems).readTLV() // NegoDataItem (SEQUENCE)
	if err != nil {
		return nil, err
	}
	tag, val, err := newBERDecoder(item).readTLV() // [0] negoToken
	if err != nil {
		return nil, err
	}
	if tag != int(berContextTag(0)) {
		return nil, protoErrf("credssp: malformed NegoDataItem")
	}
	_, token, err := newBERDecoder(val).readTLV() // OCTET STRING
	if err != nil {
		return nil, err
	}
	return token, nil
}

// encodeTSCredentials builds TSCredentials { credType=1 (password),
// credentials = DER(TSPasswordCreds) } (MS-CSSP 2.2.1.2/2.2.1.2.1) — the
// delegated logon credentials sent (sealed, as authInfo) once the server's
// public key has been verified.
func encodeTSCredentials(domain, username, password string) []byte {
	var passwordCreds bytes.Buffer
	writeTSOctetString(&passwordCreds, 0, utf16LEBytes(domain))
	writeTSOctetString(&passwordCreds, 1, utf16LEBytes(username))
	writeTSOctetString(&passwordCreds, 2, utf16LEBytes(password))
	var passwordSeq bytes.Buffer
	berTLV(&passwordSeq, berTagSequence, passwordCreds.Bytes())

	var creds bytes.Buffer
	berTLV(&creds, berContextTag(0), berInteger(1)) // credType = 1 (password)
	var credentialsInner bytes.Buffer
	berTLV(&credentialsInner, berTagOctetString, passwordSeq.Bytes())
	berTLV(&creds, berContextTag(1), credentialsInner.Bytes())

	var out bytes.Buffer
	berTLV(&out, berTagSequence, creds.Bytes())
	return out.Bytes()
}

func writeTSOctetString(buf *bytes.Buffer, tagNum int, value []byte) {
	var inner bytes.Buffer
	berTLV(&inner, berTagOctetString, value)
	berTLV(buf, berContextTag(tagNum), inner.Bytes())
}

// readTSRequestPDU reads exactly one DER-encoded TSRequest from r —
// there's no outer framing, so the boundary is determined by parsing the
// SEQUENCE tag and its own (possibly multi-byte) length.
func readTSRequestPDU(r io.Reader) ([]byte, error) {
	head, err := readFull(r, 2) // tag(1) + first length byte(1)
	if err != nil {
		return nil, err
	}
	if head[0] != berTagSequence {
		return nil, protoErrf("credssp: expected TSRequest SEQUENCE tag, got 0x%02x", head[0])
	}
	var length int
	var lenBytes []byte
	if head[1]&0x80 == 0 {
		length = int(head[1])
	} else {
		n := int(head[1] & 0x7F)
		if n == 0 || n > 4 {
			return nil, protoErrf("credssp: unsupported TSRequest length form")
		}
		lenBytes, err = readFull(r, n)
		if err != nil {
			return nil, err
		}
		for _, b := range lenBytes {
			length = length<<8 | int(b)
		}
	}
	body, err := readFull(r, length)
	if err != nil {
		return nil, err
	}
	full := append(append(append([]byte{}, head...), lenBytes...), body...)
	return full, nil
}

// performCredSSP runs the full NLA handshake over c.tc (already upgraded
// to TLS by negotiateSecurity) and leaves c.tc unchanged for the RDP
// protocol proper (MCS/GCC) to continue on immediately after — CredSSP
// doesn't add any framing of its own beyond the TSRequest PDUs themselves.
func (c *Client) performCredSSP(serverCert *x509.Certificate) error {
	dbg := func(step string) {
		if debugWire {
			fmt.Fprintf(osStderr, "DEBUG credssp: %s\n", step)
		}
	}
	dbg("start")
	clientNonce, err := randomBytes(32)
	if err != nil {
		return err
	}
	publicKey := serverCert.RawSubjectPublicKeyInfo

	// Step 1: NEGOTIATE_MESSAGE.
	negotiateMsg := buildNegotiateMessage()
	dbg("sending NEGOTIATE_MESSAGE")
	if err := c.sendTSRequest(tsRequest{version: credsspVersion, negoToken: negotiateMsg, clientNonce: clientNonce}); err != nil {
		return err
	}

	// Step 2: CHALLENGE_MESSAGE -> compute and send AUTHENTICATE_MESSAGE.
	dbg("reading CHALLENGE_MESSAGE")
	resp, err := c.readTSRequest()
	if err != nil {
		return fmt.Errorf("reading challenge TSRequest: %w", err)
	}
	if resp.haveError {
		return protoErrf("credssp: server rejected negotiation (NTSTATUS 0x%08x)", resp.errorCode)
	}
	if debugWire {
		fmt.Fprintf(osStderr, "DEBUG credssp: got negoToken (%d bytes): % x\n", len(resp.negoToken), resp.negoToken)
	}
	challenge, err := parseChallengeMessage(resp.negoToken)
	if err != nil {
		return fmt.Errorf("parsing challenge message: %w", err)
	}
	if debugWire {
		fmt.Fprintf(osStderr, "DEBUG credssp: challenge flags=0x%x haveTimestamp=%v targetInfoLen=%d\n", challenge.flags, challenge.haveTimestamp, len(challenge.targetInfo))
	}

	clientChallenge8, err := randomBytes(8)
	if err != nil {
		return err
	}
	var clientChallenge [8]byte
	copy(clientChallenge[:], clientChallenge8)

	timestamp := challenge.timestamp
	if !challenge.haveTimestamp {
		timestamp = ntlmCurrentTimestamp()
	}

	ntlmV2Hash := ntowfv2(c.password, c.username, c.domain)
	ntChallengeResponse, sessionBaseKey := ntlmv2Response(ntlmV2Hash, challenge.serverChallenge, clientChallenge, timestamp, challenge.targetInfo)
	keyExchangeKey := sessionBaseKey // NTLMv2: KeyExchangeKey == SessionBaseKey

	randomSessionKey, err := randomBytes(16)
	if err != nil {
		return err
	}
	exportedSessionKey := randomSessionKey
	encryptedRandomSessionKey, err := rc4Once(keyExchangeKey, randomSessionKey)
	if err != nil {
		return err
	}

	useMIC := challenge.haveTimestamp
	authParams := ntlmAuthenticateParams{
		username:                  c.username,
		domain:                    c.domain,
		ntChallengeResponse:       ntChallengeResponse,
		encryptedRandomSessionKey: encryptedRandomSessionKey,
		negotiateFlags:            challenge.flags,
		useMIC:                    useMIC,
	}
	authenticateMsg, micOffset := buildAuthenticateMessage(authParams)
	if useMIC {
		mic := ntlmComputeMIC(exportedSessionKey, negotiateMsg, resp.negoToken, authenticateMsg, micOffset)
		copy(authenticateMsg[micOffset:micOffset+16], mic)
	}

	dbg("sending AUTHENTICATE_MESSAGE")
	if err := c.sendTSRequest(tsRequest{version: credsspVersion, negoToken: authenticateMsg, clientNonce: clientNonce}); err != nil {
		return err
	}

	sealer, err := newNTLMSealer(
		ntlmSigningKey(exportedSessionKey, ntlmClientSignMagic),
		ntlmSigningKey(exportedSessionKey, ntlmClientSealMagic),
		ntlmSigningKey(exportedSessionKey, ntlmServerSignMagic),
		ntlmSigningKey(exportedSessionKey, ntlmServerSealMagic),
	)
	if err != nil {
		return err
	}

	// Step 3: public-key channel binding (CredSSP v6: SHA-256 hash, not a
	// full echo — see the package doc comment for why only this newer
	// form is implemented).
	clientHash := sha256.Sum256(concat(credsspClientServerHashMagic, clientNonce, publicKey))
	dbg("sending pubKeyAuth")
	if err := c.sendTSRequest(tsRequest{version: credsspVersion, pubKeyAuth: sealer.wrap(clientHash[:]), clientNonce: clientNonce}); err != nil {
		return err
	}

	dbg("reading pubKeyAuth response")
	pubKeyResp, err := c.readTSRequest()
	if err != nil {
		return fmt.Errorf("reading pubKeyAuth TSRequest: %w", err)
	}
	if pubKeyResp.haveError {
		return protoErrf("credssp: server rejected authentication (NTSTATUS 0x%08x)", pubKeyResp.errorCode)
	}
	serverHash, err := sealer.unwrap(pubKeyResp.pubKeyAuth)
	if err != nil {
		return fmt.Errorf("verifying server's public key binding: %w", err)
	}
	wantServerHash := sha256.Sum256(concat(credsspServerClientHashMagic, clientNonce, publicKey))
	if !bytes.Equal(serverHash, wantServerHash[:]) {
		return protoErrf("credssp: server's public key binding does not match — possible man-in-the-middle attack")
	}
	dbg("pubKeyAuth verified")

	// Step 4: hand over the actual logon credentials, sealed.
	tsCredentials := encodeTSCredentials(c.domain, c.username, c.password)
	dbg("sending authInfo (delegated credentials)")
	if err := c.sendTSRequest(tsRequest{version: credsspVersion, authInfo: sealer.wrap(tsCredentials), clientNonce: clientNonce}); err != nil {
		return err
	}

	dbg("done")
	return nil
}

func (c *Client) sendTSRequest(req tsRequest) error {
	_, err := c.tc.Write(encodeTSRequest(req))
	return err
}

func (c *Client) readTSRequest() (tsRequest, error) {
	pdu, err := readTSRequestPDU(c.tc)
	if err != nil {
		return tsRequest{}, err
	}
	return decodeTSRequest(pdu)
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func rc4Once(key, plaintext []byte) ([]byte, error) {
	cipher, err := rc4.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(plaintext))
	cipher.XORKeyStream(out, plaintext)
	return out, nil
}
