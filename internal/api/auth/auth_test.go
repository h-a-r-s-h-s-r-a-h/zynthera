package auth

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// Cheap settings so the tests run fast.
var testParams = Argon2Params{MemoryKiB: 64, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("correct horse battery", testParams)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=64,t=1,p=1$") {
		t.Errorf("not a PHC string: %s", hash)
	}

	ok, err := VerifyPassword("correct horse battery", hash)
	if err != nil || !ok {
		t.Errorf("correct password rejected: ok=%v err=%v", ok, err)
	}
	ok, err = VerifyPassword("wrong password!", hash)
	if err != nil || ok {
		t.Errorf("wrong password accepted: ok=%v err=%v", ok, err)
	}
}

func TestHashPasswordUsesFreshSalt(t *testing.T) {
	a, _ := HashPassword("same password", testParams)
	b, _ := HashPassword("same password", testParams)
	if a == b {
		t.Error("two hashes of the same password are identical")
	}
}

func TestVerifyPasswordWithOtherParams(t *testing.T) {
	// A hash made with older, different settings must still verify.
	old := Argon2Params{MemoryKiB: 32, Iterations: 2, Parallelism: 2, SaltLength: 8, KeyLength: 16}
	hash, err := HashPassword("an old password", old)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := VerifyPassword("an old password", hash)
	if err != nil || !ok {
		t.Errorf("ok=%v err=%v", ok, err)
	}
}

func TestVerifyPasswordRejectsBadHashes(t *testing.T) {
	bad := []string{
		"",
		"plaintext",
		"$argon2i$v=19$m=64,t=1,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=18$m=64,t=1,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=64,t=1$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=64,t=1,p=1$!!!$aGFzaA",
		"$argon2id$v=19$m=64,t=1,p=1$c2FsdA$!!!",
		"$argon2id$v=19$m=0,t=1,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=4294967295,t=1,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=64,t=9999,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=64,t=1,p=0$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=64,t=1,p=1$$aGFzaA",
	}
	for _, h := range bad {
		ok, err := VerifyPassword("x", h)
		if ok || !errors.Is(err, ErrInvalidHash) {
			t.Errorf("hash %q: ok=%v err=%v", h, ok, err)
		}
	}
}

func TestNewToken(t *testing.T) {
	plain, hash, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if !LooksLikeToken(plain) {
		t.Errorf("token %q fails its own format check", plain)
	}
	if !bytes.Equal(hash, HashToken(plain)) {
		t.Error("returned hash does not match HashToken")
	}
	if len(hash) != 32 {
		t.Errorf("hash length = %d", len(hash))
	}
	other, _, _ := NewToken()
	if other == plain {
		t.Error("two tokens are identical")
	}
}

func TestLooksLikeToken(t *testing.T) {
	for _, bad := range []string{"", "short", strings.Repeat("a", 44), strings.Repeat("!", 43)} {
		if LooksLikeToken(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}
