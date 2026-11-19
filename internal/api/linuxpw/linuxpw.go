// Package linuxpw hashes the Linux account password of a new VM with SHA-512
// crypt, the "$6$" format that Linux and cloud-init accept. The plain
// password exists only inside the request handler; only this hash is stored
// and sent to the host.
package linuxpw

import (
	"crypto/rand"
	"fmt"

	"github.com/GehirnInc/crypt/sha512_crypt"
)

// The characters crypt(3) allows in a salt.
const saltAlphabet = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// Hash returns "$6$<salt>$<hash>" for the password, with a fresh random salt.
func Hash(password string) (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("linuxpw: random salt: %w", err)
	}
	salt := make([]byte, 16)
	for i, b := range raw {
		salt[i] = saltAlphabet[int(b)%len(saltAlphabet)]
	}
	return HashWithSalt(password, string(salt))
}

// HashWithSalt is Hash with a given salt. It exists for tests.
func HashWithSalt(password, salt string) (string, error) {
	hash, err := sha512_crypt.New().Generate([]byte(password), []byte("$6$"+salt))
	if err != nil {
		return "", fmt.Errorf("linuxpw: %w", err)
	}
	return hash, nil
}
