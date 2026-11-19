// Package secrets encrypts the host credentials stored in the database, so a
// leaked database dump alone does not give access to the VM hosts.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

// Box encrypts and decrypts with AES-256-GCM under one key (ZYN_SECRETS_KEY).
type Box struct {
	aead cipher.AEAD
}

// New returns a Box for a 32-byte key.
func New(key []byte) (*Box, error) {
	if len(key) != 32 {
		return nil, errors.New("secrets: key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secrets: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secrets: %w", err)
	}
	return &Box{aead: aead}, nil
}

// Seal encrypts plaintext. The result is nonce || ciphertext, with a fresh
// random nonce every time.
func (b *Box) Seal(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("secrets: nonce: %w", err)
	}
	return b.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Open decrypts a value made by Seal. It fails if the value was changed or
// was sealed with another key.
func (b *Box) Open(sealed []byte) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n {
		return nil, errors.New("secrets: value is too short")
	}
	plaintext, err := b.aead.Open(nil, sealed[:n], sealed[n:], nil)
	if err != nil {
		return nil, errors.New("secrets: cannot decrypt (wrong ZYN_SECRETS_KEY or damaged value)")
	}
	return plaintext, nil
}

// NewSecret returns 32 random bytes as a base64url string, for agent tokens
// and stream secrets.
func NewSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("secrets: random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
