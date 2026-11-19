package secrets

import (
	"bytes"
	"testing"
)

func key(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }

func TestSealOpenRoundTrip(t *testing.T) {
	box, err := New(key(1))
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := box.Seal([]byte("agent-token"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("agent-token")) {
		t.Fatal("sealed value contains the plaintext")
	}
	got, err := box.Open(sealed)
	if err != nil || string(got) != "agent-token" {
		t.Fatalf("Open = %q, %v", got, err)
	}
}

func TestSealUsesAFreshNonce(t *testing.T) {
	box, _ := New(key(1))
	a, _ := box.Seal([]byte("same"))
	b, _ := box.Seal([]byte("same"))
	if bytes.Equal(a, b) {
		t.Fatal("two seals of the same value are identical")
	}
}

func TestOpenRejectsTamperingAndWrongKey(t *testing.T) {
	box, _ := New(key(1))
	sealed, _ := box.Seal([]byte("agent-token"))

	tampered := append([]byte{}, sealed...)
	tampered[len(tampered)-1] ^= 0x01
	if _, err := box.Open(tampered); err == nil {
		t.Error("tampered value accepted")
	}

	other, _ := New(key(2))
	if _, err := other.Open(sealed); err == nil {
		t.Error("value opened with the wrong key")
	}

	if _, err := box.Open([]byte("short")); err == nil {
		t.Error("too-short value accepted")
	}
}

func TestNewRejectsBadKeyLength(t *testing.T) {
	if _, err := New([]byte("too short")); err == nil {
		t.Fatal("short key accepted")
	}
}

func TestNewSecret(t *testing.T) {
	a, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewSecret()
	if a == b || len(a) != 43 {
		t.Fatalf("secrets: %q %q", a, b)
	}
}
