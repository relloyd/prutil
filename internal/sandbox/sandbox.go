// Package sandbox starts coding agents inside their vendor's own sandbox, and
// asks whether one already running is inside it.
//
// Each vendor ships an OS-level sandbox that confines the commands an agent
// runs rather than the agent itself, so a contained agent stays an ordinary
// process in its pane and herdr still recognises it. What differs between
// vendors is everything else: how the sandbox is switched on, where its policy
// lives, and whether the vendor can say that it is on. A Profile holds those
// answers for one kind of agent, and Sandbox is the registry prutil consults.
//
// Claude Code verifies its policy with the vendor's status command. Copilot
// and agy have no equivalent: their profiles infer posture from launch
// arguments and the reader's settings, refusing to claim containment when
// required settings are absent.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/relloyd/prutil/internal/run"
)

// Posture is what could be established about one agent's sandbox.
type Posture struct {
	// Known says the vendor answered. An agent nobody could ask about is not
	// a contained one, and is never treated as one.
	Known bool
	// Contained says the vendor confirmed the sandbox or its launch switch
	// and effective policy establish containment.
	Contained bool
	// Strict says the agent cannot run a command outside the sandbox, not
	// even with a person's approval.
	Strict bool
	// Detail says in a phrase what was found, for the handoff log and for the
	// reader when prutil refuses to go on.
	Detail string
}

// String is how the handoff log and the WATCH page name a posture.
func (p Posture) String() string {
	switch {
	case !p.Known:
		return "sandbox unknown"
	case p.Contained && p.Strict:
		return "sandboxed, strict"
	case p.Contained:
		return "sandboxed"
	}
	return "not sandboxed"
}

// Target is where an agent is about to be started.
type Target struct {
	// Dir is the checkout it will work in. A vendor's effective settings
	// include that project's own, so this is where a profile asks about them.
	Dir string
	// Host and Owner name the pull request's repository on GitHub, for the
	// parts of a policy that have to be written for it.
	Host  string
	Owner string
}

// Launch is how to start one agent contained.
type Launch struct {
	// Args are the agent's own command-line arguments.
	Args []string
	// Posture is what could be established before starting the agent.
	Posture Posture
}

// Env is the machine a profile writes its policy for.
type Env struct {
	// Dir is where profiles keep their policy files: beneath prutil's home.
	Dir string
	// Home is the reader's home directory, for paths a policy denies.
	Home string
	// SSHAuthSock is the SSH agent's socket as prutil found it at startup.
	SSHAuthSock string
	// RealPath resolves symbolic links. Nil means filepath.EvalSymlinks; tests
	// replace it.
	RealPath func(path string) (string, error)
}

func (e Env) realPath(path string) (string, error) {
	if e.RealPath != nil {
		return e.RealPath(path)
	}
	return filepath.EvalSymlinks(path)
}

// Runner starts a vendor's CLI in a directory. run.Cmd is the real one.
type Runner interface {
	RunIn(ctx context.Context, dir string, args ...string) ([]byte, error)
}

// Profile is how prutil contains one kind of agent. Adding a vendor means
// adding one of these; nothing outside this package changes.
type Profile struct {
	// Kind is the herdr agent kind: "claude", "copilot", "agy".
	Kind string
	// Bin is the vendor's CLI, looked up on PATH the first time a hook needs
	// it.
	Bin string
	// Launch writes whatever policy the agent's arguments refer to and returns
	// those arguments, with the posture the vendor says they give. Nil means
	// prutil does not know how to contain this kind yet: it is started as it
	// always was, and security.require_sandbox does not apply to it.
	Launch func(ctx context.Context, env Env, cli Runner, t Target) (Launch, error)
	// Inspect reports the posture of an agent already running in dir, launched
	// with argv. Nil means the vendor cannot say, and such an agent is never
	// counted as contained.
	Inspect func(ctx context.Context, env Env, cli Runner, dir string, argv []string) (Posture, error)
}

// Profiles is every kind prutil has an entry for, contained or not yet.
func Profiles() []Profile {
	return []Profile{claude(), copilot(), agy()}
}

// inspectTTL is how long one answer about a running agent is reused. Asking
// is a process per candidate per handoff, and an agent's launch arguments do
// not change while it runs.
const inspectTTL = time.Minute

// Options wires a Sandbox.
type Options struct {
	Env Env
	// Look finds a vendor's CLI. Nil means run.Look.
	Look func(bin string) (Runner, error)
	// Profiles replaces the built-in ones, for tests.
	Profiles []Profile
	// Now is the clock the answer cache reads. Nil means time.Now.
	Now func() time.Time
}

// Sandbox is the registry of profiles, and a short memory of what running
// agents were found to be.
type Sandbox struct {
	env      Env
	look     func(bin string) (Runner, error)
	profiles map[string]Profile
	now      func() time.Time

	mu    sync.Mutex
	cache map[string]cachedPosture
}

type cachedPosture struct {
	posture Posture
	at      time.Time
}

// New builds a Sandbox.
func New(opts Options) *Sandbox {
	s := &Sandbox{env: opts.Env, look: opts.Look, now: opts.Now, cache: map[string]cachedPosture{}}
	if s.look == nil {
		s.look = func(bin string) (Runner, error) { return run.Look(bin, "") }
	}
	if s.now == nil {
		s.now = time.Now
	}
	profiles := opts.Profiles
	if profiles == nil {
		profiles = Profiles()
	}
	s.profiles = make(map[string]Profile, len(profiles))
	for _, p := range profiles {
		s.profiles[p.Kind] = p
	}
	return s
}

// ErrUnsupported is returned by Launch for a kind prutil cannot contain yet.
var ErrUnsupported = errors.New("prutil has no sandbox profile for this kind of agent")

// Supports reports whether prutil can start this kind of agent contained.
func (s *Sandbox) Supports(kind string) bool {
	if s == nil {
		return false
	}
	p, ok := s.profiles[kind]
	return ok && p.Launch != nil
}

// Launch prepares the policy for one agent and returns the arguments that
// start it contained.
func (s *Sandbox) Launch(ctx context.Context, kind string, t Target) (Launch, error) {
	if !s.Supports(kind) {
		return Launch{}, fmt.Errorf("%w: %s", ErrUnsupported, kind)
	}
	p := s.profiles[kind]
	cli, err := s.look(p.Bin)
	if err != nil {
		return Launch{}, err
	}
	return p.Launch(ctx, s.env, cli, t)
}

// Inspect reports the posture of an agent already running in dir, launched
// with argv. Answers are reused for inspectTTL; failures are not, so a vendor
// that could not answer is asked again next time.
func (s *Sandbox) Inspect(ctx context.Context, kind, dir string, argv []string) (Posture, error) {
	if s == nil {
		return Posture{Detail: "prutil has no sandbox registry"}, nil
	}
	p, ok := s.profiles[kind]
	if !ok || p.Inspect == nil {
		return Posture{Detail: "prutil cannot ask " + kind + " whether it is sandboxed"}, nil
	}

	key := kind + "\x00" + dir + "\x00" + strings.Join(argv, "\x00")
	s.mu.Lock()
	hit, found := s.cache[key]
	s.mu.Unlock()
	if found && s.now().Sub(hit.at) < inspectTTL {
		return hit.posture, nil
	}

	cli, err := s.look(p.Bin)
	if err != nil {
		return Posture{}, err
	}
	posture, err := p.Inspect(ctx, s.env, cli, dir, argv)
	if err != nil {
		return Posture{}, err
	}
	s.mu.Lock()
	s.cache[key] = cachedPosture{posture: posture, at: s.now()}
	s.mu.Unlock()
	return posture, nil
}
