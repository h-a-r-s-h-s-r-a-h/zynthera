// Package logging sets up structured JSON logging for every binary.
package logging

import (
	"io"
	"log/slog"
	"os"
)

// New returns a JSON logger tagged with the service name.
func New(service string) *slog.Logger {
	return NewTo(os.Stdout, service)
}

// NewTo is New with a custom destination, used by tests.
func NewTo(w io.Writer, service string) *slog.Logger {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})
	return slog.New(h).With("service", service)
}
