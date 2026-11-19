package token

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var (
	secret = []byte("host-stream-secret")
	now    = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
)

const (
	vmID   = "6f1c0b1e-0000-4000-8000-000000000001"
	userID = "0a0a0a0a-0000-4000-8000-000000000002"
)

func mustSign(t *testing.T, purpose string, ttl time.Duration) string {
	t.Helper()
	tok, expires, err := Sign(secret, vmID, userID, purpose, ttl, now)
	if err != nil {
		t.Fatal(err)
	}
	if !expires.Equal(now.Add(ttl)) {
		t.Fatalf("expires = %v", expires)
	}
	return tok
}

func TestSignAndVerify(t *testing.T) {
	tok := mustSign(t, PurposeStream, time.Minute)
	c, err := Verify(secret, tok, PurposeStream, now.Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if c.VMID != vmID || c.UserID != userID || c.Purpose != PurposeStream || c.ID == "" {
		t.Errorf("claims = %+v", c)
	}
	// The token must be safe to put in a URL query without escaping.
	if strings.ContainsAny(tok, "+/= &?#") {
		t.Errorf("token is not URL-safe: %s", tok)
	}
}

func TestExpiry(t *testing.T) {
	tok := mustSign(t, PurposeStream, time.Minute)
	if _, err := Verify(secret, tok, PurposeStream, now.Add(59*time.Second)); err != nil {
		t.Errorf("one second before expiry: %v", err)
	}
	for _, at := range []time.Duration{time.Minute, time.Minute + time.Second, time.Hour} {
		if _, err := Verify(secret, tok, PurposeStream, now.Add(at)); !errors.Is(err, ErrExpired) {
			t.Errorf("at +%v: error = %v, want ErrExpired", at, err)
		}
	}
}

func TestWrongPurpose(t *testing.T) {
	files := mustSign(t, PurposeFiles, time.Minute)
	if _, err := Verify(secret, files, PurposeStream, now); !errors.Is(err, ErrWrongPurpose) {
		t.Errorf("files token used for a stream: %v", err)
	}
	stream := mustSign(t, PurposeStream, time.Minute)
	if _, err := Verify(secret, stream, PurposeFiles, now); !errors.Is(err, ErrWrongPurpose) {
		t.Errorf("stream token used for files: %v", err)
	}
}

func TestTampering(t *testing.T) {
	tok := mustSign(t, PurposeStream, time.Minute)
	payload, sig, _ := strings.Cut(tok, ".")

	// A payload naming another VM, with the original signature.
	forged, _, err := Sign([]byte("attacker-secret"), "99999999-0000-4000-8000-000000000009", userID, PurposeStream, time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	forgedPayload, forgedSig, _ := strings.Cut(forged, ".")

	cases := map[string]string{
		"other payload, original signature":  forgedPayload + "." + sig,
		"original payload, other signature":  payload + "." + forgedSig,
		"signed with another secret":         forged,
		"one character of payload changed":   flip(payload) + "." + sig,
		"one character of signature changed": payload + "." + flip(sig),
		"signature removed":                  payload + ".",
		"no dot":                             payload,
		"empty":                              "",
		"not base64":                         "!!!.???",
		"extra part":                         tok + ".x",
	}
	for name, bad := range cases {
		if _, err := Verify(secret, bad, PurposeStream, now); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := Verify(nil, tok, PurposeStream, now); err == nil {
		t.Error("verified with an empty secret")
	}
	if _, err := Verify(secret, strings.Repeat("a", 2000), PurposeStream, now); !errors.Is(err, ErrMalformed) {
		t.Error("oversized token not rejected as malformed")
	}
}

// flip changes the first character to a different valid base64url character.
func flip(s string) string {
	if s[0] == 'A' {
		return "B" + s[1:]
	}
	return "A" + s[1:]
}

func TestSignRefusesAnEmptySecret(t *testing.T) {
	if _, _, err := Sign(nil, vmID, userID, PurposeStream, time.Minute, now); err == nil {
		t.Fatal("signed with an empty secret")
	}
}

func TestReplay(t *testing.T) {
	used := NewUsedSet()
	tok := mustSign(t, PurposeStream, time.Minute)
	c, err := Verify(secret, tok, PurposeStream, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := used.Use(c, now); err != nil {
		t.Fatalf("first use: %v", err)
	}
	if err := used.Use(c, now.Add(time.Second)); !errors.Is(err, ErrReplayed) {
		t.Fatalf("second use: %v, want ErrReplayed", err)
	}
	// A different token for the same VM is fine.
	other, _ := Verify(secret, mustSign(t, PurposeStream, time.Minute), PurposeStream, now)
	if err := used.Use(other, now); err != nil {
		t.Fatalf("another token: %v", err)
	}
	if used.Len() != 2 {
		t.Fatalf("remembered ids = %d", used.Len())
	}
	// Ids are forgotten once their tokens have expired.
	late, _ := Verify(secret, mustSignAt(t, now.Add(2*time.Minute)), PurposeStream, now.Add(2*time.Minute))
	if err := used.Use(late, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if used.Len() != 1 {
		t.Fatalf("expired ids were not forgotten: %d remembered", used.Len())
	}
}

func mustSignAt(t *testing.T, at time.Time) string {
	t.Helper()
	tok, _, err := Sign(secret, vmID, userID, PurposeStream, time.Minute, at)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestTokensAreUnique(t *testing.T) {
	first := mustSign(t, PurposeStream, time.Minute)
	second := mustSign(t, PurposeStream, time.Minute)
	if first == second {
		t.Fatal("two tokens for the same VM are identical")
	}
}
