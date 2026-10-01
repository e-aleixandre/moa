package push

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Native push: end-to-end encrypted envelopes for the iOS app, delivered by a
// stateless relay that holds the APNs key. The byte-level contract lives in
// relay/PROTOCOL.md and is pinned by relay/test/vectors.json.

const (
	// EnvelopeSize is the padded plaintext size: every envelope is the same
	// length, so its size says nothing about its content.
	EnvelopeSize = 2048
	// envelopeTTL is how long the NSE accepts an envelope (and APNs keeps it).
	envelopeTTL = 12 * time.Hour

	labelEnc      = "moa-push-enc-v1"
	labelSend     = "moa-push-send-v1"
	labelCollapse = "moa-push-collapse-v1"
	labelKID      = "moa-push-kid-v1"
	aadEnvelope   = "moa-push-v1"
	macSend       = "moa-send-v1"
	macCollapse   = "collapse-v1"
	collapseLen   = 22
)

// b64u is base64url without padding, the encoding of every binary field.
var b64u = base64.RawURLEncoding

var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// DeviceKeys are derived from the 32-byte secret S the iPhone generated.
type DeviceKeys struct {
	Enc      []byte // AES-256-GCM key of the envelope
	Send     []byte // HMAC key that authorizes sends to this device
	Collapse []byte // HMAC key of the opaque grouping ids
	KID      []byte // 8 bytes naming the key inside the envelope
}

// DeriveDeviceKeys expands S with HKDF-SHA256 (empty salt, label as info).
func DeriveDeviceKeys(secret []byte) (DeviceKeys, error) {
	if len(secret) != 32 {
		return DeviceKeys{}, errors.New("push secret must be 32 bytes")
	}
	derive := func(label string, n int) []byte {
		k, err := hkdf.Key(sha256.New, secret, nil, label, n)
		if err != nil {
			panic(err) // only fails for lengths beyond 255*32
		}
		return k
	}
	return DeviceKeys{
		Enc:      derive(labelEnc, 32),
		Send:     derive(labelSend, 32),
		Collapse: derive(labelCollapse, 32),
		KID:      derive(labelKID, 8),
	}, nil
}

// EnvelopeContent is what the NSE reads once the envelope authenticates.
// Field order is the marshalling order; the NSE does not depend on it.
type EnvelopeContent struct {
	V      int    `json:"v"`
	ID     string `json:"id"`
	Exp    int64  `json:"exp"`
	Dest   string `json:"d"`
	Sess   string `json:"s,omitempty"`
	Kind   Kind   `json:"k"`
	Level  Level  `json:"lvl"`
	Thread string `json:"th"`
	Title  string `json:"t"`
	Body   string `json:"b"`
}

// Envelope is the encrypted payload as it travels inside the relay request
// and the APNs payload.
type Envelope struct {
	V   int    `json:"v"`
	KID string `json:"k"`
	C   string `json:"c"`
}

// SealEnvelope encrypts n for one device. now dates it; the envelope expires
// after envelopeTTL.
func SealEnvelope(keys DeviceKeys, n Notification, now time.Time) (Envelope, error) {
	id := make([]byte, 16)
	nonce := make([]byte, 12)
	if _, err := rand.Read(id); err != nil {
		return Envelope{}, err
	}
	if _, err := rand.Read(nonce); err != nil {
		return Envelope{}, err
	}
	return sealEnvelope(keys, n, now, b64u.EncodeToString(id), nonce)
}

func sealEnvelope(keys DeviceKeys, n Notification, now time.Time, id string, nonce []byte) (Envelope, error) {
	plain, err := EnvelopeContenttext(keys, n, now, id)
	if err != nil {
		return Envelope{}, err
	}
	gcm, err := newGCM(keys.Enc)
	if err != nil {
		return Envelope{}, err
	}
	aad := append([]byte(aadEnvelope), keys.KID...)
	sealed := gcm.Seal(append([]byte(nil), nonce...), nonce, plain, aad)
	return Envelope{V: 1, KID: b64u.EncodeToString(keys.KID), C: b64u.EncodeToString(sealed)}, nil
}

// EnvelopeContenttext builds the padded JSON. Texts are cut on UTF-8 boundaries
// until the JSON fits: escaping can grow a string, so it is measured after
// marshalling, not before.
func EnvelopeContenttext(keys DeviceKeys, n Notification, now time.Time, id string) ([]byte, error) {
	p := EnvelopeContent{
		V:     1,
		ID:    id,
		Exp:   now.Add(envelopeTTL).Unix(),
		Dest:  "home",
		Kind:  n.Kind,
		Level: n.Level,
		Title: n.Title,
		Body:  n.Body,
	}
	switch {
	case n.SessionID != "" && sessionIDPattern.MatchString(n.SessionID):
		p.Dest, p.Sess = "session", n.SessionID
	case n.Inbox:
		p.Dest = "inbox"
	}
	if p.Level == "" {
		p.Level = LevelActive
	}
	p.Thread = collapseID(keys, n.Tag)
	p.Title = cutUTF8(p.Title, 256)
	p.Body = cutUTF8(p.Body, 1024)
	for {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(p); err != nil {
			return nil, err
		}
		out := bytes.TrimRight(buf.Bytes(), "\n")
		if len(out) <= EnvelopeSize {
			padded := make([]byte, EnvelopeSize)
			copy(padded, out)
			for i := len(out); i < EnvelopeSize; i++ {
				padded[i] = ' '
			}
			return padded, nil
		}
		switch {
		case p.Body != "":
			p.Body = cutUTF8(p.Body, len(p.Body)/2)
		case p.Title != "":
			p.Title = cutUTF8(p.Title, len(p.Title)/2)
		default:
			return nil, errors.New("push envelope does not fit")
		}
	}
}

// cutUTF8 shortens s to at most n bytes without splitting a rune.
func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// collapseID is the opaque grouping id of a policy tag ("scope:id"): equal
// tags of one device give equal ids, and nothing else is revealed.
func collapseID(keys DeviceKeys, tag string) string {
	if tag == "" {
		return ""
	}
	scope, id, _ := strings.Cut(tag, ":")
	mac := hmac.New(sha256.New, keys.Collapse)
	mac.Write([]byte(macCollapse))
	mac.Write([]byte{0})
	mac.Write([]byte(scope))
	mac.Write([]byte{0})
	mac.Write([]byte(id))
	return b64u.EncodeToString(mac.Sum(nil))[:collapseLen]
}

// sendRequest is the relay /v1/send body.
type sendRequest struct {
	Handle   string   `json:"h"`
	Time     int64    `json:"t"`
	Collapse string   `json:"c,omitempty"`
	Envelope Envelope `json:"e"`
}

// BuildSend returns the exact /v1/send body and its X-Moa-Sig header value.
func BuildSend(keys DeviceKeys, handle string, n Notification, now time.Time) (body []byte, sig string, err error) {
	env, err := SealEnvelope(keys, n, now)
	if err != nil {
		return nil, "", err
	}
	return buildSendBody(keys, handle, collapseID(keys, n.Tag), env, now)
}

func buildSendBody(keys DeviceKeys, handle, collapse string, env Envelope, now time.Time) ([]byte, string, error) {
	body, err := json.Marshal(sendRequest{Handle: handle, Time: now.Unix(), Collapse: collapse, Envelope: env})
	if err != nil {
		return nil, "", err
	}
	return body, SignSend(keys.Send, body), nil
}

// SignSend authorizes exactly these body bytes.
func SignSend(sendKey, body []byte) string {
	digest := sha256.Sum256(body)
	mac := hmac.New(sha256.New, sendKey)
	mac.Write([]byte(macSend))
	mac.Write([]byte{0})
	mac.Write(digest[:])
	return b64u.EncodeToString(mac.Sum(nil))
}

// OpenEnvelope is the NSE side, here for tests and as a reference: it
// authenticates before reading anything.
func OpenEnvelope(keys DeviceKeys, env Envelope, now time.Time) (EnvelopeContent, error) {
	var p EnvelopeContent
	if env.V != 1 || env.KID != b64u.EncodeToString(keys.KID) {
		return p, errors.New("unknown envelope version or key")
	}
	raw, err := b64u.DecodeString(env.C)
	if err != nil || len(raw) != 12+EnvelopeSize+16 {
		return p, errors.New("malformed envelope")
	}
	gcm, err := newGCM(keys.Enc)
	if err != nil {
		return p, err
	}
	plain, err := gcm.Open(nil, raw[:12], raw[12:], append([]byte(aadEnvelope), keys.KID...))
	if err != nil {
		return p, errors.New("envelope does not authenticate")
	}
	if err := json.Unmarshal(plain, &p); err != nil {
		return p, err
	}
	if p.V != 1 || p.Exp < now.Unix() {
		return EnvelopeContent{}, errors.New("envelope version unknown or expired")
	}
	return p, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
