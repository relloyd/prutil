// Package herdr talks to a running herdr server by shelling out to the herdr
// CLI, which speaks the same local socket API the terminal itself uses.
//
// herdr organises terminals into workspaces, tabs and panes, recognises the
// coding agent occupying a pane and reports its lifecycle state. prutil uses
// that to hand pull request feedback to whichever agent is already sitting in
// the right branch, rather than asking the reader to find it themselves.
//
// Every command replies with a JSON envelope: a result on standard output and
// exit status zero, or an error object on standard error and exit status one.
// Both shapes are unwrapped here so that callers see typed values and typed
// errors, never the envelope.
package herdr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/relloyd/prutil/internal/run"
)

// Binary is the CLI prutil shells out to, and Home the address named when it
// is missing.
const (
	Binary = "herdr"
	Home   = "https://herdr.dev"
)

// Agent lifecycle states, as herdr reports them. idle means ready for input
// and seen in the focused UI; done is the same underlying state after unseen
// background work finished; blocked means herdr recognised an approval or
// question dialog; unknown means an agent is present but herdr will not
// classify it, which is not the same as finished.
const (
	StatusIdle    = "idle"
	StatusWorking = "working"
	StatusBlocked = "blocked"
	StatusDone    = "done"
	StatusUnknown = "unknown"
)

// Error codes prutil has to tell apart. A blocked agent must never be prompted,
// because herdr rejects the submission before sending anything and because
// answering somebody else's approval dialog is not prutil's business.
const (
	CodeAgentBlocked       = "agent_blocked"
	CodeAgentNotReady      = "agent_not_ready"
	CodeAgentPromptStalled = "agent_prompt_stalled"
	CodeAgentNotFound      = "agent_not_found"
)

// Controller is the herdr surface the rest of prutil depends on. Keeping it an
// interface lets the TUI tests run without a herdr server.
type Controller interface {
	// Agents lists every agent herdr currently recognises.
	Agents(ctx context.Context) ([]Agent, error)
	// Agent re-reads one agent, addressed by pane id or by live agent name.
	Agent(ctx context.Context, target string) (Agent, error)
	// Prompt submits text to an agent, followed by Enter.
	Prompt(ctx context.Context, target, text string) error
	// Notify shows a toast in the herdr UI.
	Notify(ctx context.Context, title, body string) error
}

// Agent is one recognised coding agent occupying a pane.
type Agent struct {
	// Kind is the agent herdr detected, such as "claude" or "copilot".
	Kind string `json:"agent"`
	// Name is the handle given by agent start. It is empty for an agent herdr
	// merely detected, which is why PaneID is what prutil addresses.
	Name string `json:"name"`
	// Status is one of the lifecycle states above.
	Status string `json:"agent_status"`
	// CWD is the directory the pane was started in; ForegroundCWD is where its
	// foreground process is now, which is the more accurate of the two when an
	// agent has moved.
	CWD           string `json:"cwd"`
	ForegroundCWD string `json:"foreground_cwd"`

	PaneID      string `json:"pane_id"`
	TabID       string `json:"tab_id"`
	WorkspaceID string `json:"workspace_id"`
	Focused     bool   `json:"focused"`
	Title       string `json:"terminal_title_stripped"`
}

// Target is how this agent is addressed on the command line. The pane id is
// always present and always unique, where a name is only set for an agent
// herdr was asked to start.
func (a Agent) Target() string {
	if a.PaneID != "" {
		return a.PaneID
	}
	return a.Name
}

// Dir is the directory the agent is working in.
func (a Agent) Dir() string {
	if a.ForegroundCWD != "" {
		return a.ForegroundCWD
	}
	return a.CWD
}

// Settled reports whether the agent is ready to be given work.
func (a Agent) Settled() bool {
	return a.Status == StatusIdle || a.Status == StatusDone
}

// Client is a Controller backed by the herdr CLI.
type Client struct {
	run run.Runner
}

// New builds a client over any runner, which is what the tests use.
func New(r run.Runner) *Client { return &Client{run: r} }

// NewExec locates the herdr binary on PATH. A *run.NotInstalledError means
// herdr is not installed, which is a reason to disable the feature rather than
// to fail: prutil is useful without it.
func NewExec() (*Client, error) {
	cmd, err := run.Look(Binary, Home)
	if err != nil {
		return nil, err
	}
	return New(cmd), nil
}

// Available reports whether a herdr server is running and answering. The
// cheapest question that proves it is a listing, so that is what it asks.
func (c *Client) Available(ctx context.Context) error {
	_, err := c.Agents(ctx)
	return err
}

// Agents implements Controller.
func (c *Client) Agents(ctx context.Context) ([]Agent, error) {
	var result struct {
		Agents []Agent `json:"agents"`
	}
	if err := c.call(ctx, &result, "agent", "list"); err != nil {
		return nil, err
	}
	return result.Agents, nil
}

// Agent implements Controller.
func (c *Client) Agent(ctx context.Context, target string) (Agent, error) {
	var result struct {
		Agent Agent `json:"agent"`
	}
	if err := c.call(ctx, &result, "agent", "get", target); err != nil {
		return Agent{}, err
	}
	return result.Agent, nil
}

// Prompt implements Controller. It deliberately does not wait: a handoff is
// work the agent will take minutes over, and prutil has a terminal to keep
// redrawing in the meantime.
func (c *Client) Prompt(ctx context.Context, target, text string) error {
	return c.call(ctx, nil, "agent", "prompt", target, text)
}

// Notify implements Controller. The request sound is used because a toast
// nobody hears is not a handoff anybody acts on.
func (c *Client) Notify(ctx context.Context, title, body string) error {
	args := []string{"notification", "show", title, "--sound", "request"}
	if body != "" {
		args = append(args, "--body", body)
	}
	return c.call(ctx, nil, args...)
}

// StartAgent launches a supported agent in a pane that is already sitting at
// an interactive shell prompt. herdr returns only once it has detected the
// agent in that same pane and considers it ready for input.
func (c *Client) StartAgent(ctx context.Context, name, kind, pane string, timeout time.Duration) (Agent, error) {
	args := []string{"agent", "start", name, "--kind", kind, "--pane", pane}
	if timeout > 0 {
		args = append(args, "--timeout", strconv.Itoa(int(timeout.Milliseconds())))
	}
	var result struct {
		Agent Agent `json:"agent"`
	}
	if err := c.call(ctx, &result, args...); err != nil {
		return Agent{}, err
	}
	return result.Agent, nil
}

// call runs one herdr command and unwraps its envelope into result, which may
// be nil when the reply carries nothing worth reading.
func (c *Client) call(ctx context.Context, result any, args ...string) error {
	out, err := c.run.Run(ctx, args...)
	if err != nil {
		// A server error is JSON on standard error with exit status one, so
		// the exit status alone does not say what went wrong. The envelope
		// does, and its code is what callers switch on.
		if apiErr := apiErrorFrom(err); apiErr != nil {
			return apiErr
		}
		return err
	}

	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *APIError       `json:"error"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		return fmt.Errorf("herdr %s: could not read the reply: %w", strings.Join(args, " "), err)
	}
	if env.Error != nil {
		return env.Error
	}
	if result == nil || len(env.Result) == 0 {
		return nil
	}
	if err := json.Unmarshal(env.Result, result); err != nil {
		return fmt.Errorf("herdr %s: could not read the reply: %w", strings.Join(args, " "), err)
	}
	return nil
}

// APIError is the error object a herdr command returns.
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Error implements error.
func (e *APIError) Error() string {
	if e.Message == "" {
		return "herdr: " + e.Code
	}
	return "herdr: " + e.Message
}

// apiErrorFrom digs the error envelope out of a failed invocation's standard
// error, returning nil when the failure was not one the server described.
func apiErrorFrom(err error) *APIError {
	var runErr *run.Error
	if !errors.As(err, &runErr) || runErr.Stderr == "" {
		return nil
	}
	// Standard error that is not the server's envelope leaves the field nil,
	// which is the same answer as an envelope carrying no error: this failure
	// was not one the server described, so the caller keeps what it has.
	var env struct {
		Error *APIError `json:"error"`
	}
	_ = json.Unmarshal([]byte(runErr.Stderr), &env)
	return env.Error
}

// Code returns the herdr error code carried by err, or the empty string when
// err did not come from the server.
func Code(err error) string {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Code
	}
	return ""
}
