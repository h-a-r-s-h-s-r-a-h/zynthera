// Package run executes the host tools the agent relies on (virsh, qemu-img,
// cloud-localds, zfs). Commands are always an argument list handed straight
// to the program: nothing ever passes through a shell, so no value can be
// interpreted as shell syntax.
package run

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Error is a command that ran and failed. Its text includes what the
// command wrote to stderr, which is usually the only useful explanation.
type Error struct {
	Name     string
	Args     []string
	ExitCode int
	Stderr   string
}

func (e *Error) Error() string {
	stderr := strings.TrimSpace(e.Stderr)
	if stderr == "" {
		stderr = "(no error output)"
	}
	return fmt.Sprintf("%s %s: exit %d: %s", e.Name, strings.Join(e.Args, " "), e.ExitCode, stderr)
}

// StderrContains reports whether err is a command failure whose error
// output contains any of the given fragments (compared in lower case).
// Drivers use it to recognise "already in that condition" answers.
func StderrContains(err error, fragments ...string) bool {
	var re *Error
	if !errors.As(err, &re) {
		return false
	}
	text := strings.ToLower(re.Stderr)
	for _, f := range fragments {
		if strings.Contains(text, strings.ToLower(f)) {
			return true
		}
	}
	return false
}

// Runner runs commands. Tests replace it with a fake.
type Runner interface {
	// Output runs the command and returns its standard output.
	Output(ctx context.Context, name string, args ...string) (string, error)
}

// Exec is the real Runner.
type Exec struct{}

// Output implements Runner.
func (Exec) Output(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // name is a fixed tool; args are validated by callers and never reach a shell
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err == nil {
		return stdout.String(), nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), ctxErr)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return stdout.String(), &Error{Name: name, Args: args, ExitCode: exit.ExitCode(), Stderr: stderr.String()}
	}
	// The program could not be started at all (not installed, not executable).
	return "", fmt.Errorf("start %s: %w", name, err)
}
