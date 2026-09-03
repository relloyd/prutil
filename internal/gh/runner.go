// Package gh talks to GitHub by shelling out to the gh CLI. Authentication is
// therefore gh's problem, not ours: it resolves credentials from its own
// keyring or config, honours GH_TOKEN and GITHUB_TOKEN when they are set, and
// supports GitHub Enterprise via GH_HOST. No token ever passes through prutil.
package gh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ErrNotInstalled is returned when the gh CLI is not on PATH.
var ErrNotInstalled = errors.New("the gh CLI is required but was not found on PATH: see https://cli.github.com")

// Runner executes gh with the given arguments and returns its standard output.
// It exists so that tests can drive the client from fixtures without running a
// real process.
type Runner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

// ExecRunner runs the real gh binary.
type ExecRunner struct {
	Path string
}

// NewExecRunner locates gh on PATH, returning ErrNotInstalled when it is
// missing.
func NewExecRunner() (*ExecRunner, error) {
	path, err := exec.LookPath("gh")
	if err != nil {
		return nil, ErrNotInstalled
	}
	return &ExecRunner{Path: path}, nil
}

// Run implements Runner.
func (r *ExecRunner) Run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, r.Path, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), &CommandError{
			Args:   args,
			Stderr: strings.TrimSpace(stderr.String()),
			Err:    err,
		}
	}
	return stdout.Bytes(), nil
}

// CommandError reports a gh invocation that exited non-zero, keeping gh's own
// diagnostics so they can be shown to the user verbatim.
type CommandError struct {
	Args   []string
	Stderr string
	Err    error
}

// Error implements error.
func (e *CommandError) Error() string {
	if e.Stderr != "" {
		return fmt.Sprintf("gh %s: %s", strings.Join(summarise(e.Args), " "), e.Stderr)
	}
	return fmt.Sprintf("gh %s: %v", strings.Join(summarise(e.Args), " "), e.Err)
}

// Unwrap implements errors.Unwrap.
func (e *CommandError) Unwrap() error { return e.Err }

// summarise trims the GraphQL document out of an argument list so that error
// messages stay to one line.
func summarise(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if strings.HasPrefix(a, "query=") {
			out = append(out, "query=...")
			continue
		}
		out = append(out, a)
	}
	return out
}
