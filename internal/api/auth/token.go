package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// NewToken returns 32 random bytes as a base64url string, plus the SHA-256
// hash that is stored in the database. The plain value goes to the user (in
// a cookie or an emailed link) and is never stored.
func NewToken() (plain string, hash []byte, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, fmt.Errorf("generate token: %w", err)
	}
	plain = base64.RawURLEncoding.EncodeToString(b)
	return plain, HashToken(plain), nil
}

// HashToken returns the SHA-256 hash of a token's plain value.
func HashToken(plain string) []byte {
	sum := sha256.Sum256([]byte(plain))
	return sum[:]
}

// LooksLikeToken is a cheap format check done before touching the database.
func LooksLikeToken(plain string) bool {
	if len(plain) != 43 {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(plain)
	return err == nil
}
