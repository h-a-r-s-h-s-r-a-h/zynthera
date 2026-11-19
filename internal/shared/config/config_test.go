package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDotEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "# comment\n\nZYN_T_PLAIN=hello\nZYN_T_QUOTED=\"with space\"\nZYN_T_ANGLE=Zynthera <no-reply@zynthera.local>\nZYN_T_KEEP=from-file\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZYN_T_KEEP", "from-env")
	for _, k := range []string{"ZYN_T_PLAIN", "ZYN_T_QUOTED", "ZYN_T_ANGLE"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}

	if err := LoadDotEnv(path); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"ZYN_T_PLAIN":  "hello",
		"ZYN_T_QUOTED": "with space",
		"ZYN_T_ANGLE":  "Zynthera <no-reply@zynthera.local>",
		"ZYN_T_KEEP":   "from-env",
	}
	for k, v := range want {
		if got := os.Getenv(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

func TestLoadDotEnvMissingFileIsFine(t *testing.T) {
	if err := LoadDotEnv(filepath.Join(t.TempDir(), "nope")); err != nil {
		t.Fatal(err)
	}
}

func TestLoadDotEnvBadLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("not a pair\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadDotEnv(path); err == nil {
		t.Fatal("expected an error")
	}
}

func TestLoaderCollectsEveryError(t *testing.T) {
	t.Setenv("ZYN_T_INT", "abc")
	t.Setenv("ZYN_T_LIST", "1,x")
	t.Setenv("ZYN_T_MODE", "weird")
	t.Setenv("ZYN_T_KEY", "short")
	os.Unsetenv("ZYN_T_REQ")

	var l Loader
	if got := l.Int("ZYN_T_INT", 7); got != 7 {
		t.Errorf("Int fallback = %d", got)
	}
	l.IntList("ZYN_T_LIST", nil)
	l.OneOf("ZYN_T_MODE", "a", "a", "b")
	l.Key32("ZYN_T_KEY")
	l.Required("ZYN_T_REQ")

	err := l.Err()
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, k := range []string{"ZYN_T_INT", "ZYN_T_LIST", "ZYN_T_MODE", "ZYN_T_KEY", "ZYN_T_REQ"} {
		if !strings.Contains(err.Error(), k) {
			t.Errorf("error does not mention %s: %v", k, err)
		}
	}
}

func TestLoaderHappyPath(t *testing.T) {
	t.Setenv("ZYN_T_INT", "42")
	t.Setenv("ZYN_T_LIST", "1, 2,4")
	// 32 bytes of 'a', base64.
	t.Setenv("ZYN_T_KEY", "YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFhYWE=")

	var l Loader
	if got := l.Int("ZYN_T_INT", 1); got != 42 {
		t.Errorf("Int = %d", got)
	}
	list := l.IntList("ZYN_T_LIST", nil)
	if len(list) != 3 || list[2] != 4 {
		t.Errorf("IntList = %v", list)
	}
	if key := l.Key32("ZYN_T_KEY"); len(key) != 32 {
		t.Errorf("Key32 length = %d", len(key))
	}
	if got := l.String("ZYN_T_UNSET_XYZ", "def"); got != "def" {
		t.Errorf("String default = %q", got)
	}
	if err := l.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestNotPlaceholder(t *testing.T) {
	var l Loader
	l.NotPlaceholder("A", []byte("real-secret"))
	if l.Err() != nil {
		t.Fatal("real secret flagged")
	}
	l.NotPlaceholder("B", []byte("zyn-dev-only-token"))
	if l.Err() == nil {
		t.Fatal("placeholder not flagged")
	}
}
