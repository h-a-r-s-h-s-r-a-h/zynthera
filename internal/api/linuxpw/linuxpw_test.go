package linuxpw

import (
	"strings"
	"testing"

	"zynthera/internal/shared/vmtypes"
)

// Reference values produced by `openssl passwd -6 -salt <salt> <password>`.
func TestMatchesOpenSSL(t *testing.T) {
	cases := []struct{ password, salt, want string }{
		{"correct horse", "abcdefgh", "$6$abcdefgh$yIZAF3gQPvtKZO/9qOJKffAKKbtS3ef3qmwyugk4uWVjX8YZf/GV3A8SkFxEPY0T56CcilGrHKLffBsp6dLMG."},
		{"p@ss wörd/with$odd'chars", "ZynTheraSalt0123", "$6$ZynTheraSalt0123$ZnOs6hktUAfEIRkFNzd9Jx60BftWs/pyar2nF5lOoIMPLBw5TBwjKbDQbyw.pr1w7sJRJ4TxMr4ZpM15ZBL/c1"},
	}
	for _, tc := range cases {
		got, err := HashWithSalt(tc.password, tc.salt)
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("HashWithSalt(%q, %q)\n got  %s\n want %s", tc.password, tc.salt, got, tc.want)
		}
	}
}

func TestHashIsAcceptedByTheAgent(t *testing.T) {
	a, err := Hash("a linux password")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Hash("a linux password")
	if a == b {
		t.Error("two hashes share a salt")
	}
	if !strings.HasPrefix(a, "$6$") || strings.Contains(a, "a linux password") {
		t.Errorf("unexpected hash: %s", a)
	}
	// The agent validates every spec; the hash must pass its pattern.
	spec := vmtypes.VMSpec{
		ID: "11111111-1111-4111-8111-111111111111", ImageSlug: "ubuntu-24.04-xfce", ImageVersion: "2026.11.1",
		RAMMB: 2048, VCPUs: 1, DiskGB: 40, Hostname: "zynthera-111111", Username: "tester",
		PasswordHash: a, BridgeToken: strings.Repeat("a", 40),
	}
	if msg := spec.Validate(); msg != "" {
		t.Errorf("agent would reject the hash: %s", msg)
	}
}
