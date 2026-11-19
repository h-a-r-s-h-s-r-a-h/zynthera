// Package config holds small helpers for reading configuration from
// environment variables. Each binary defines its own typed config struct and
// fills it with a Loader, which collects every problem so the process can stop
// with one clear message listing all of them.
package config

import (
	"bufio"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
)

// PlaceholderMarker appears in every development-only secret in .env.example.
// Production refuses to start when a secret still contains it.
const PlaceholderMarker = "dev-only"

// LoadDotEnv reads KEY=VALUE lines from path into the process environment.
// Variables that are already set are left alone, so the real environment
// always wins. A missing file is not an error.
func LoadDotEnv(path string) error {
	f, err := os.Open(path) //nolint:gosec // the path is chosen by the program, never by a user
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		key, value, ok := strings.Cut(text, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return fmt.Errorf("%s:%d: expected KEY=VALUE", path, line)
		}
		value = unquote(strings.TrimSpace(value))
		if _, set := os.LookupEnv(key); set {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("%s:%d: %w", path, line, err)
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	return nil
}

func unquote(v string) string {
	if len(v) >= 2 {
		first, last := v[0], v[len(v)-1]
		if (first == '"' || first == '\'') && first == last {
			return v[1 : len(v)-1]
		}
	}
	return v
}

// Loader reads typed values from the environment and remembers every error.
type Loader struct {
	errs []string
}

func (l *Loader) fail(format string, args ...any) {
	l.errs = append(l.errs, fmt.Sprintf(format, args...))
}

// Fail records a validation error found by the caller.
func (l *Loader) Fail(format string, args ...any) { l.fail(format, args...) }

// String returns the variable, or def when it is unset or empty.
func (l *Loader) String(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Required returns the variable and records an error when it is missing.
func (l *Loader) Required(key string) string {
	v := os.Getenv(key)
	if v == "" {
		l.fail("%s is required", key)
	}
	return v
}

// OneOf returns the variable and records an error unless it is an allowed value.
func (l *Loader) OneOf(key, def string, allowed ...string) string {
	v := l.String(key, def)
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	l.fail("%s must be one of %s, got %q", key, strings.Join(allowed, ", "), v)
	return v
}

// Int returns the variable as a positive integer, or def when unset.
func (l *Loader) Int(key string, def int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		l.fail("%s must be a non-negative whole number, got %q", key, raw)
		return def
	}
	return n
}

// IntList parses a comma-separated list of positive integers.
func (l *Loader) IntList(key string, def []int) []int {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	var out []int
	for _, part := range strings.Split(raw, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || n <= 0 {
			l.fail("%s must be a comma-separated list of positive numbers, got %q", key, raw)
			return def
		}
		out = append(out, n)
	}
	return out
}

// Key32 returns a base64-encoded 32-byte key.
func (l *Loader) Key32(key string) []byte {
	raw := l.Required(key)
	if raw == "" {
		return nil
	}
	b, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(b) != 32 {
		l.fail("%s must be 32 random bytes encoded as base64 (generate with: openssl rand -base64 32)", key)
		return nil
	}
	return b
}

// NotPlaceholder records an error when a secret still holds its development
// placeholder. Call it only when running in production.
func (l *Loader) NotPlaceholder(key string, value []byte) {
	if strings.Contains(string(value), PlaceholderMarker) {
		l.fail("%s still has its development placeholder; set a real secret", key)
	}
}

// Err returns all recorded problems as one error, or nil.
func (l *Loader) Err() error {
	if len(l.errs) == 0 {
		return nil
	}
	return errors.New("invalid configuration:\n  - " + strings.Join(l.errs, "\n  - "))
}
