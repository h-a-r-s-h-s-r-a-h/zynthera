// Package auth holds the security primitives for accounts: password hashing,
// random tokens and rate limiting.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Password length limits (spec section 10.2).
const (
	MinPasswordLength = 10
	MaxPasswordLength = 128
)

// Argon2Params are the cost settings for argon2id. They are stored inside
// every hash, so they can be raised later without breaking existing passwords.
type Argon2Params struct {
	MemoryKiB   uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

// DefaultArgon2Params follows the OWASP baseline for argon2id.
var DefaultArgon2Params = Argon2Params{
	MemoryKiB:   19 * 1024,
	Iterations:  2,
	Parallelism: 1,
	SaltLength:  16,
	KeyLength:   32,
}

// Upper bounds accepted when reading a stored hash, so a corrupted or hostile
// value cannot make the server allocate huge amounts of memory.
const (
	maxMemoryKiB  = 1024 * 1024
	maxIterations = 20
	maxKeyLength  = 128
)

// ErrInvalidHash means a stored password hash could not be parsed.
var ErrInvalidHash = errors.New("auth: invalid password hash")

// HashPassword returns an argon2id hash in the standard PHC string format:
// $argon2id$v=19$m=19456,t=2,p=1$<salt>$<hash>
func HashPassword(password string, p Argon2Params) (string, error) {
	salt := make([]byte, p.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, p.Iterations, p.MemoryKiB, p.Parallelism, p.KeyLength)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.MemoryKiB, p.Iterations, p.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword reports whether password matches the stored hash.
func VerifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, ErrInvalidHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, ErrInvalidHash
	}
	var memory, iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return false, ErrInvalidHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, ErrInvalidHash
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, ErrInvalidHash
	}
	if memory == 0 || memory > maxMemoryKiB || iterations == 0 || iterations > maxIterations ||
		parallelism == 0 || len(salt) == 0 || len(want) == 0 || len(want) > maxKeyLength {
		return false, ErrInvalidHash
	}

	got := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, uint32(len(want))) //nolint:gosec // length is bounded above
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
