// Package token implements the short-lived signed tokens a browser presents
// to a host agent (spec section 10.9). The API signs them; the agent, which
// has no database, verifies them with the host's stream secret.
//
// Format: base64url(JSON payload) + "." + base64url(HMAC-SHA256(payload bytes, secret))
package token

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Purposes. A token is only valid for the purpose it was issued for.
const (
	PurposeStream = "stream"
	PurposeFiles  = "files"
)

const version = 1

// Claims is the token payload.
type Claims struct {
	V       int    `json:"v"`
	ID      string `json:"jti"`
	VMID    string `json:"vm"`
	UserID  string `json:"uid"`
	Purpose string `json:"pur"`
	Expires int64  `json:"exp"` // Unix seconds
}

// Errors from Verify. They all mean "reject"; the distinction is for logs
// and tests.
var (
	ErrMalformed    = errors.New("token: malformed")
	ErrSignature    = errors.New("token: bad signature")
	ErrExpired      = errors.New("token: expired")
	ErrWrongPurpose = errors.New("token: wrong purpose")
	ErrReplayed     = errors.New("token: already used")
)

var enc = base64.RawURLEncoding

func sign(payload, secret []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	return mac.Sum(nil)
}

// Sign issues a token for one VM and purpose that expires after ttl.
func Sign(secret []byte, vmID, userID, purpose string, ttl time.Duration, now time.Time) (tok string, expires time.Time, err error) {
	if len(secret) == 0 {
		return "", time.Time{}, errors.New("token: empty secret")
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", time.Time{}, fmt.Errorf("token: random id: %w", err)
	}
	expires = now.Add(ttl).Truncate(time.Second)
	payload, err := json.Marshal(Claims{
		V: version, ID: hex.EncodeToString(id[:]), VMID: vmID, UserID: userID, Purpose: purpose, Expires: expires.Unix(),
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("token: encode: %w", err)
	}
	return enc.EncodeToString(payload) + "." + enc.EncodeToString(sign(payload, secret)), expires, nil
}

// Verify checks the signature, the expiry and the purpose, and returns the
// claims. The signature is checked first and in constant time, so nothing
// inside an unsigned payload is ever trusted.
func Verify(secret []byte, tok, purpose string, now time.Time) (Claims, error) {
	if len(secret) == 0 || len(tok) > 1024 {
		return Claims{}, ErrMalformed
	}
	payloadPart, sigPart, ok := strings.Cut(tok, ".")
	if !ok {
		return Claims{}, ErrMalformed
	}
	payload, err := enc.DecodeString(payloadPart)
	if err != nil {
		return Claims{}, ErrMalformed
	}
	sig, err := enc.DecodeString(sigPart)
	if err != nil {
		return Claims{}, ErrMalformed
	}
	if !hmac.Equal(sig, sign(payload, secret)) {
		return Claims{}, ErrSignature
	}
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil || c.V != version || c.ID == "" || c.VMID == "" {
		return Claims{}, ErrMalformed
	}
	if now.Unix() >= c.Expires {
		return Claims{}, ErrExpired
	}
	if c.Purpose != purpose {
		return Claims{}, ErrWrongPurpose
	}
	return c, nil
}

// UsedSet remembers the ids of single-use tokens until they expire. The
// agent uses it so a stream token works for exactly one connection.
type UsedSet struct {
	mu   sync.Mutex
	used map[string]int64 // token id -> expiry (Unix seconds)
}

// NewUsedSet returns an empty set.
func NewUsedSet() *UsedSet { return &UsedSet{used: map[string]int64{}} }

// Use marks the token as used. It returns ErrReplayed if it was used before.
func (u *UsedSet) Use(c Claims, now time.Time) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	// Forget ids whose tokens have expired anyway. The set stays small:
	// tokens live for a minute.
	for id, exp := range u.used {
		if exp <= now.Unix() {
			delete(u.used, id)
		}
	}
	if _, seen := u.used[c.ID]; seen {
		return ErrReplayed
	}
	u.used[c.ID] = c.Expires
	return nil
}

// Len reports how many ids are remembered. It exists for tests.
func (u *UsedSet) Len() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.used)
}
