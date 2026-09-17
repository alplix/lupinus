package rdp

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/rc4"
	"encoding/binary"
	"strings"
	"time"
	"unicode/utf16"

	"golang.org/x/crypto/md4" //nolint:staticcheck // required by NTLM (MS-NLMP), not a choice

	"bytes"
)

// NTLM (MS-NLMP), from scratch — the authentication mechanism CredSSP/NLA
// (credssp.go) carries inside its TSRequest negoTokens. Ported against
// FreeRDP's winpr/libwinpr/sspi/NTLM/{ntlm_message.c,ntlm_compute.c} and
// winpr/libwinpr/utils/ntlm.c for exact byte layout and the NTLMv2/signing
// key derivation formulas, which — like Tight's compression-control byte
// and RDP's field-presence order encoding — aren't written down anywhere
// more authoritative than "what the reference implementation does".
//
// Scope: NTLMv2 only (no NTLMv1, no Kerberos), Extended Session Security
// always on, always requesting confidentiality (NTLMSSP_NEGOTIATE_SEAL) —
// CredSSP needs it to wrap pubKeyAuth/authInfo regardless of whether the
// RDP session itself ends up using it for anything else.

const ntlmSignature = "NTLMSSP\x00"

const (
	ntlmMessageTypeNegotiate    = 1
	ntlmMessageTypeChallenge    = 2
	ntlmMessageTypeAuthenticate = 3
)

const (
	ntlmNegotiate56                      = 0x80000000
	ntlmNegotiateKeyExch                 = 0x40000000
	ntlmNegotiate128                     = 0x20000000
	ntlmNegotiateVersion                 = 0x02000000
	ntlmNegotiateTargetInfo              = 0x00800000
	ntlmNegotiateExtendedSessionSecurity = 0x00080000
	ntlmNegotiateAlwaysSign              = 0x00008000
	ntlmNegotiateNTLM                    = 0x00000200
	ntlmNegotiateLMKey                   = 0x00000080
	ntlmNegotiateSeal                    = 0x00000020
	ntlmNegotiateSign                    = 0x00000010
	ntlmRequestTarget                    = 0x00000004
	ntlmNegotiateOEM                     = 0x00000002
	ntlmNegotiateUnicode                 = 0x00000001
)

// AV_PAIR ids (MS-NLMP 2.2.2.1) inside a CHALLENGE message's TargetInfo.
const (
	msvAvEOL       = 0
	msvAvTimestamp = 7
)

// ntlmClientSignMagic/etc. are MD5'd together with the exported session
// key to derive the four signing/sealing keys (MS-NLMP 3.4.5.2) — the
// trailing NUL matters, C's sizeof(string literal) includes it and that's
// what every real implementation hashes.
var (
	ntlmClientSignMagic = []byte("session key to client-to-server signing key magic constant\x00")
	ntlmServerSignMagic = []byte("session key to server-to-client signing key magic constant\x00")
	ntlmClientSealMagic = []byte("session key to client-to-server sealing key magic constant\x00")
	ntlmServerSealMagic = []byte("session key to server-to-client sealing key magic constant\x00")
)

func utf16LEBytes(s string) []byte {
	units := utf16.Encode([]rune(s))
	out := make([]byte, 2*len(units))
	for i, u := range units {
		binary.LittleEndian.PutUint16(out[2*i:], u)
	}
	return out
}

// ntowfv2 computes the NTLMv2 password hash: HMAC-MD5(MD4(UTF16LE(password)),
// UTF16LE(UPPERCASE(username)) || UTF16LE(domain)).
func ntowfv2(password, username, domain string) []byte {
	h := md4.New()
	h.Write(utf16LEBytes(password))
	ntHashV1 := h.Sum(nil)

	mac := hmac.New(md5.New, ntHashV1)
	mac.Write(utf16LEBytes(strings.ToUpper(username)))
	mac.Write(utf16LEBytes(domain))
	return mac.Sum(nil)
}

// --- message field helpers (the 8-byte Len/MaxLen/Offset structure every
// variable-length NTLM message field uses) ---

func writeMessageFields(buf *bytes.Buffer, length, offset int) {
	writeUint16LE(buf, uint16(length))
	writeUint16LE(buf, uint16(length))
	writeUint32LE(buf, uint32(offset))
}

func readMessageFields(b []byte) (length, offset int, err error) {
	if len(b) < 8 {
		return 0, 0, protoErrf("ntlm: message field too short")
	}
	length = int(binary.LittleEndian.Uint16(b[0:2]))
	offset = int(binary.LittleEndian.Uint32(b[4:8]))
	return length, offset, nil
}

// buildNegotiateMessage constructs the NTLM NEGOTIATE_MESSAGE (MS-NLMP
// 2.2.1.1) — no domain/workstation supplied, matching how every modern
// client behaves (those fields are a legacy NTLMv1 accommodation).
func buildNegotiateMessage() []byte {
	flags := uint32(ntlmNegotiate56 | ntlmNegotiateVersion | ntlmNegotiateLMKey | ntlmNegotiateOEM |
		ntlmNegotiateKeyExch | ntlmNegotiate128 | ntlmNegotiateExtendedSessionSecurity |
		ntlmNegotiateAlwaysSign | ntlmNegotiateNTLM | ntlmNegotiateSign | ntlmNegotiateSeal |
		ntlmRequestTarget | ntlmNegotiateUnicode)

	var b bytes.Buffer
	b.WriteString(ntlmSignature)
	writeUint32LE(&b, ntlmMessageTypeNegotiate)
	writeUint32LE(&b, flags)
	writeMessageFields(&b, 0, 32) // DomainName: empty
	writeMessageFields(&b, 0, 32) // Workstation: empty
	writeNTLMVersion(&b)
	return b.Bytes()
}

// writeNTLMVersion writes a fixed, plausible VERSION structure (MS-NLMP
// 2.2.2.10) — servers don't act on its contents, it's purely informational,
// but its presence is expected whenever NTLMSSP_NEGOTIATE_VERSION is set.
func writeNTLMVersion(b *bytes.Buffer) {
	b.WriteByte(10)          // ProductMajorVersion
	b.WriteByte(0)           // ProductMinorVersion
	writeUint16LE(b, 19041)  // ProductBuild
	b.Write(make([]byte, 3)) // Reserved
	b.WriteByte(0x0F)        // NTLMRevisionCurrent: NTLMSSP_REVISION_W2K3
}

type ntlmChallenge struct {
	serverChallenge [8]byte
	targetInfo      []byte // raw AV_PAIR blob, kept as-is for the NTLMv2 response
	timestamp       [8]byte
	haveTimestamp   bool
	flags           uint32
}

// parseChallengeMessage reads an NTLM CHALLENGE_MESSAGE (MS-NLMP 2.2.1.2).
// TargetName is parsed only far enough to skip over it; this client
// doesn't need its contents (no UI surfaces the realm/domain name during
// NLA in v0.2.0's scope).
func parseChallengeMessage(data []byte) (*ntlmChallenge, error) {
	if len(data) < 12 || string(data[0:8]) != ntlmSignature {
		return nil, protoErrf("ntlm: not a valid NTLMSSP message")
	}
	if binary.LittleEndian.Uint32(data[8:12]) != ntlmMessageTypeChallenge {
		return nil, protoErrf("ntlm: expected CHALLENGE_MESSAGE")
	}
	if len(data) < 12+8+4+8+8+8 {
		return nil, protoErrf("ntlm: CHALLENGE_MESSAGE too short")
	}
	off := 12
	off += 8 // TargetNameFields: not needed
	flags := binary.LittleEndian.Uint32(data[off:])
	off += 4
	var ch ntlmChallenge
	ch.flags = flags
	copy(ch.serverChallenge[:], data[off:off+8])
	off += 8
	off += 8 // Reserved
	tiLen, tiOffset, err := readMessageFields(data[off:])
	if err != nil {
		return nil, err
	}
	off += 8
	if flags&ntlmNegotiateVersion != 0 {
		off += 8 // Version
	}
	_ = off // payload offsets below are absolute, taken from tiOffset directly

	if tiLen > 0 {
		if tiOffset < 0 || tiOffset+tiLen > len(data) {
			return nil, protoErrf("ntlm: TargetInfo out of range")
		}
		ch.targetInfo = data[tiOffset : tiOffset+tiLen]
		ts, ok := ntlmFindAVPair(ch.targetInfo, msvAvTimestamp)
		if ok && len(ts) >= 8 {
			copy(ch.timestamp[:], ts[:8])
			ch.haveTimestamp = true
		}
	}
	return &ch, nil
}

// ntlmFindAVPair scans a TargetInfo AV_PAIR list (MS-NLMP 2.2.2.1: each
// entry AvId(2 LE) + AvLen(2 LE) + value, terminated by MsvAvEOL) for the
// first entry with the given id.
func ntlmFindAVPair(targetInfo []byte, id uint16) ([]byte, bool) {
	pos := 0
	for pos+4 <= len(targetInfo) {
		avID := binary.LittleEndian.Uint16(targetInfo[pos:])
		avLen := int(binary.LittleEndian.Uint16(targetInfo[pos+2:]))
		pos += 4
		if avID == msvAvEOL {
			break
		}
		if pos+avLen > len(targetInfo) {
			break
		}
		if avID == id {
			return targetInfo[pos : pos+avLen], true
		}
		pos += avLen
	}
	return nil, false
}

// ntlmv2Response computes NTChallengeResponse and the SessionBaseKey
// (MS-NLMP 3.3.2, "Compute NTLMv2 Response"). clientChallenge is 8 random
// bytes; timestamp is either the server's own (if it sent one) or the
// current time.
func ntlmv2Response(ntlmV2Hash []byte, serverChallenge [8]byte, clientChallenge [8]byte, timestamp [8]byte, targetInfo []byte) (ntChallengeResponse, sessionBaseKey []byte) {
	temp := make([]byte, 28+len(targetInfo))
	temp[0] = 1 // RespType
	temp[1] = 1 // HiRespType
	// Reserved1(2), Reserved2(4) already zero
	copy(temp[8:16], timestamp[:])
	copy(temp[16:24], clientChallenge[:])
	// Reserved3(4) already zero
	copy(temp[28:], targetInfo)

	mac := hmac.New(md5.New, ntlmV2Hash)
	mac.Write(serverChallenge[:])
	mac.Write(temp)
	ntProofStr := mac.Sum(nil)

	ntChallengeResponse = append(append([]byte{}, ntProofStr...), temp...)

	mac2 := hmac.New(md5.New, ntlmV2Hash)
	mac2.Write(ntProofStr)
	sessionBaseKey = mac2.Sum(nil)
	return ntChallengeResponse, sessionBaseKey
}

func ntlmSigningKey(exportedSessionKey, magic []byte) []byte {
	h := md5.New()
	h.Write(exportedSessionKey)
	h.Write(magic)
	return h.Sum(nil)
}

// ntlmAuthenticateParams bundles everything buildAuthenticateMessage needs
// beyond what's already implicit in the challenge.
type ntlmAuthenticateParams struct {
	username, domain          string
	ntChallengeResponse       []byte
	encryptedRandomSessionKey []byte
	negotiateFlags            uint32
	useMIC                    bool
}

// buildAuthenticateMessage constructs the NTLM AUTHENTICATE_MESSAGE
// (MS-NLMP 2.2.1.3). Returns the message bytes and, if useMIC, the byte
// offset the (still-zero) MIC field starts at, so the caller can compute
// MIC = HMAC-MD5(ExportedSessionKey, Negotiate||Challenge||Authenticate)
// with this exact serialization and patch it in afterward.
func buildAuthenticateMessage(p ntlmAuthenticateParams) (msg []byte, micOffset int) {
	usernameW := utf16LEBytes(p.username)
	domainW := utf16LEBytes(p.domain)
	lmChallengeResponse := make([]byte, 24) // NTLMv2: LM response is unused, sent as zeros

	fixedLen := 64
	if p.negotiateFlags&ntlmNegotiateVersion != 0 {
		fixedLen += 8
	}
	if p.useMIC {
		fixedLen += 16
	}

	domainOffset := fixedLen
	usernameOffset := domainOffset + len(domainW)
	workstationOffset := usernameOffset + len(usernameW)
	lmOffset := workstationOffset // no workstation
	ntOffset := lmOffset + len(lmChallengeResponse)
	keyOffset := ntOffset + len(p.ntChallengeResponse)

	var b bytes.Buffer
	b.WriteString(ntlmSignature)
	writeUint32LE(&b, ntlmMessageTypeAuthenticate)
	writeMessageFields(&b, len(lmChallengeResponse), lmOffset)
	writeMessageFields(&b, len(p.ntChallengeResponse), ntOffset)
	writeMessageFields(&b, len(domainW), domainOffset)
	writeMessageFields(&b, len(usernameW), usernameOffset)
	writeMessageFields(&b, 0, workstationOffset) // Workstation: empty
	writeMessageFields(&b, len(p.encryptedRandomSessionKey), keyOffset)
	writeUint32LE(&b, p.negotiateFlags)
	if p.negotiateFlags&ntlmNegotiateVersion != 0 {
		writeNTLMVersion(&b)
	}
	if p.useMIC {
		micOffset = b.Len()
		b.Write(make([]byte, 16))
	}
	b.Write(domainW)
	b.Write(usernameW)
	// Workstation: nothing to write (empty)
	b.Write(lmChallengeResponse)
	b.Write(p.ntChallengeResponse)
	b.Write(p.encryptedRandomSessionKey)

	return b.Bytes(), micOffset
}

func ntlmComputeMIC(exportedSessionKey, negotiateMsg, challengeMsg, authenticateMsg []byte, micOffset int) []byte {
	mac := hmac.New(md5.New, exportedSessionKey)
	mac.Write(negotiateMsg)
	mac.Write(challengeMsg)
	mac.Write(authenticateMsg[:micOffset])
	mac.Write(make([]byte, 16))
	mac.Write(authenticateMsg[micOffset+16:])
	return mac.Sum(nil)
}

// ntlmSealer implements NTLM's GSS_WrapEx/GSS_UnwrapEx (MS-NLMP 3.4.3) —
// confidentiality+integrity for CredSSP's pubKeyAuth/authInfo TSRequest
// fields. One RC4 keystream per direction, continued (not reset) across
// every wrap/unwrap call for the lifetime of the security context, per
// spec — this is why sendCipher/recvCipher are stored, not recreated.
type ntlmSealer struct {
	sendSigningKey, recvSigningKey []byte
	sendCipher, recvCipher         *rc4.Cipher
	sendSeq, recvSeq               uint32
}

func newNTLMSealer(clientSigningKey, clientSealingKey, serverSigningKey, serverSealingKey []byte) (*ntlmSealer, error) {
	sendCipher, err := rc4.NewCipher(clientSealingKey)
	if err != nil {
		return nil, err
	}
	recvCipher, err := rc4.NewCipher(serverSealingKey)
	if err != nil {
		return nil, err
	}
	return &ntlmSealer{
		sendSigningKey: clientSigningKey,
		recvSigningKey: serverSigningKey,
		sendCipher:     sendCipher,
		recvCipher:     recvCipher,
	}, nil
}

// wrap seals plaintext, returning signature(16 bytes) || sealed data —
// exactly the wire format CredSSP embeds as pubKeyAuth/authInfo.
func (s *ntlmSealer) wrap(plaintext []byte) []byte {
	mac := hmac.New(md5.New, s.sendSigningKey)
	var seq [4]byte
	binary.LittleEndian.PutUint32(seq[:], s.sendSeq)
	mac.Write(seq[:])
	mac.Write(plaintext)
	digest := mac.Sum(nil)

	sealed := make([]byte, len(plaintext))
	s.sendCipher.XORKeyStream(sealed, plaintext)

	checksum := make([]byte, 8)
	s.sendCipher.XORKeyStream(checksum, digest[:8]) // same continuing RC4 stream

	out := make([]byte, 16+len(sealed))
	binary.LittleEndian.PutUint32(out[0:4], 1) // version
	copy(out[4:12], checksum)
	binary.LittleEndian.PutUint32(out[12:16], s.sendSeq)
	copy(out[16:], sealed)

	s.sendSeq++
	return out
}

// unwrap reverses wrap and verifies the signature, matching what the peer
// computed with its own send keys (our recv keys).
func (s *ntlmSealer) unwrap(wrapped []byte) ([]byte, error) {
	if len(wrapped) < 16 {
		return nil, protoErrf("ntlm: sealed message too short")
	}
	sig := wrapped[:16]
	sealed := wrapped[16:]

	data := make([]byte, len(sealed))
	s.recvCipher.XORKeyStream(data, sealed)

	mac := hmac.New(md5.New, s.recvSigningKey)
	var seq [4]byte
	binary.LittleEndian.PutUint32(seq[:], s.recvSeq)
	mac.Write(seq[:])
	mac.Write(data)
	digest := mac.Sum(nil)

	checksum := make([]byte, 8)
	s.recvCipher.XORKeyStream(checksum, digest[:8])

	if !bytes.Equal(checksum, sig[4:12]) {
		return nil, protoErrf("ntlm: message signature verification failed")
	}
	s.recvSeq++
	return data, nil
}

func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

// ntlmCurrentTimestamp returns the current time as an MS-NLMP TIMESTAMP:
// a little-endian FILETIME (100ns intervals since 1601-01-01 UTC). Only
// used as a fallback when the server's CHALLENGE_MESSAGE didn't include
// its own MsvAvTimestamp AV_PAIR — every server actually observed sends
// one, but the spec allows omitting it.
func ntlmCurrentTimestamp() [8]byte {
	const epochDiff = 116444736000000000 // 1601-01-01 to 1970-01-01, in 100ns units
	filetime := uint64(time.Now().UnixNano()/100) + epochDiff
	var out [8]byte
	binary.LittleEndian.PutUint64(out[:], filetime)
	return out
}
