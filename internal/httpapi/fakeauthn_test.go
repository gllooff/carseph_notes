package httpapi

// A fake CTAP2-style authenticator for driving WebAuthn ceremonies in tests.
// It holds one ECDSA P-256 credential, produces "none"-format attestation
// objects and ES256 assertion signatures exactly as a browser would serialize
// them (base64url fields inside PublicKeyCredential JSON).

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"

	"github.com/fxamacker/cbor/v2"
	"github.com/go-webauthn/webauthn/protocol"
)

const (
	flagUP = 0x01
	flagUV = 0x04
	flagAT = 0x40 // attested credential data included
)

type fakeAuthn struct {
	rpID   string
	origin string

	credID []byte
	key    *ecdsa.PrivateKey
	aaguid [16]byte

	signCount  uint32
	userHandle []byte
}

func newFakeAuthn(rpID, origin string) *fakeAuthn {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	credID := make([]byte, 32)
	if _, err := rand.Read(credID); err != nil {
		panic(err)
	}
	return &fakeAuthn{rpID: rpID, origin: origin, key: key, credID: credID}
}

// register produces the PublicKeyCredential JSON for a creation ceremony.
func (f *fakeAuthn) register(opts *protocol.PublicKeyCredentialCreationOptions) (json.RawMessage, error) {
	clientData, err := json.Marshal(map[string]any{
		"type": "webauthn.create", "challenge": b64u(opts.Challenge),
		"origin": f.origin, "crossOrigin": false,
	})
	if err != nil {
		return nil, err
	}

	f.signCount++
	coseKey, err := cbor.Marshal(map[int]any{
		1: 2, 3: -7, -1: 1,
		-2: f.key.PublicKey.X.FillBytes(make([]byte, 32)),
		-3: f.key.PublicKey.Y.FillBytes(make([]byte, 32)),
	})
	if err != nil {
		return nil, err
	}
	attested := make([]byte, 0, 18+len(f.credID)+len(coseKey))
	attested = append(attested, f.aaguid[:]...)
	lens := make([]byte, 2)
	binary.BigEndian.PutUint16(lens, uint16(len(f.credID)))
	attested = append(attested, lens...)
	attested = append(attested, f.credID...)
	attested = append(attested, coseKey...)

	authData := f.authData(flagUP|flagUV|flagAT, attested)
	attObj, err := cbor.Marshal(map[string]any{
		"fmt": "none", "attStmt": map[string]any{}, "authData": authData,
	})
	if err != nil {
		return nil, err
	}
	return credentialJSON(f.credID, map[string]any{
		"clientDataJSON":    b64u(clientData),
		"attestationObject": b64u(attObj),
	}, nil)
}

// assert produces the PublicKeyCredential JSON for login.
func (f *fakeAuthn) assert(opts *protocol.PublicKeyCredentialRequestOptions) (json.RawMessage, error) {
	f.signCount++ // strictly increasing across ceremonies
	clientData, err := json.Marshal(map[string]any{
		"type": "webauthn.get", "challenge": b64u(opts.Challenge),
		"origin": f.origin, "crossOrigin": false,
	})
	if err != nil {
		return nil, err
	}
	authData := f.authData(flagUP|flagUV, nil)

	cdHash := sha256.Sum256(clientData)
	sigInput := append(append([]byte{}, authData...), cdHash[:]...)
	sig, err := ecdsa.SignASN1(rand.Reader, f.key, hashMessage(sigInput))
	if err != nil {
		return nil, err
	}
	resp := map[string]any{
		"authenticatorData": b64u(authData),
		"clientDataJSON":    b64u(clientData),
		"signature":         b64u(sig),
	}
	if f.userHandle != nil {
		resp["userHandle"] = b64u(f.userHandle)
	}
	return credentialJSON(f.credID, resp, nil)
}

// hashMessage is the SHA-256 digest ES256 signs (authData || clientDataHash).
func hashMessage(msg []byte) []byte {
	sum := sha256.Sum256(msg)
	return sum[:]
}

// authData builds rpIdHash || flags || signCount || tail.
func (f *fakeAuthn) authData(flags byte, tail []byte) []byte {
	h := sha256.Sum256([]byte(f.rpID))
	out := make([]byte, 0, 37+len(tail))
	out = append(out, h[:]...)
	out = append(out, flags)
	count := make([]byte, 4)
	binary.BigEndian.PutUint32(count, f.signCount)
	out = append(out, count...)
	out = append(out, tail...)
	return out
}

// credentialJSON serializes a PublicKeyCredential as the browser would.
func credentialJSON(credID []byte, response map[string]any, transports []string) (json.RawMessage, error) {
	body := map[string]any{
		"id":       b64u(credID),
		"rawId":    b64u(credID),
		"type":     "public-key",
		"response": response,
	}
	if len(transports) > 0 {
		body["transports"] = transports
	}
	return json.Marshal(body)
}

// b64u is standard base64url without padding.
func b64u(b []byte) string {
	const enc = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	out := make([]byte, 0, (len(b)+2)/3*4)
	for i := 0; i < len(b); i += 3 {
		var c [3]byte
		n := copy(c[:], b[i:])
		v := uint32(c[0])<<16 | uint32(c[1])<<8 | uint32(c[2])
		out = append(out, enc[(v>>18)&0x3f], enc[(v>>12)&0x3f])
		if n > 1 {
			out = append(out, enc[(v>>6)&0x3f])
		}
		if n > 2 {
			out = append(out, enc[v&0x3f])
		}
	}
	return string(out)
}
