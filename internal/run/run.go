// Package run executes a helper binary and returns its standard output.
//
// prutil shells out to gh, to herdr and to git rather than linking a library
// for any of them, so that credentials, terminal sessions and repository state
// stay the concern of the tool that owns them. This package is the one place
// that knows how to start such a process and how to report one that failed.
package run

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Runner executes a binary with the given arguments and returns its standard
// output. It exists so that a client can be driven from fixtures in tests
// without running a real process.
type Runner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

// Cmd runs a real binary found on PATH.
type Cmd struct {
	// Bin is the name the binary was looked up under, used in error messages.
	Bin string
	// Path is the resolved absolute path.
	Path string
}

// Look finds bin on PATH. help, when set, is the URL named in the
// NotInstalledError returned when it is missing.
func Look(bin, help string) (*Cmd, error) {
	path, err := exec.LookPath(bin)
	if err != nil {
		return nil, &NotInstalledError{Bin: bin, Help: help}
	}
	return &Cmd{Bin: bin, Path: path}, nil
}

// Run implements Runner.
func (c *Cmd) Run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.Path, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// Stdout is returned alongside the error because some tools write a
		// usable reply and still exit non-zero; the caller decides.
		return stdout.Bytes(), &Error{
			Bin:    c.Bin,
			Args:   args,
			Stderr: strings.TrimSpace(stderr.String()),
			Err:    err,
		}
	}
	return stdout.Bytes(), nil
}

// NotInstalledError reports that a required binary is not on PATH.
type NotInstalledError struct {
	Bin  string
	Help string
}

// Error implements error.
func (e *NotInstalledError) Error() string {
	msg := fmt.Sprintf("the %s CLI is required but was not found on PATH", e.Bin)
	if e.Help != "" {
		msg += ": see " + e.Help
	}
	return msg
}

// Error reports an invocation that exited non-zero. It keeps the tool's own
// diagnostics so that they can be shown to the user verbatim rather than
// replaced by a guess at what went wrong.
type Error struct {
	Bin    string
	Args   []string
	Stderr string
	Err    error
}

// Error implements error.
func (e *Error) Error() string {
	if e.Stderr != "" {
		return fmt.Sprintf("%s %s: %s", e.Bin, strings.Join(e.Args, " "), e.Stderr)
	}
	return fmt.Sprintf("%s %s: %v", e.Bin, strings.Join(e.Args, " "), e.Err)
}

// Unwrap implements errors.Unwrap.
func (e *Error) Unwrap() error { return e.Err }
