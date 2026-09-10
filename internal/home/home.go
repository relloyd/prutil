// Package home owns prutil's application directory: where its configuration
// lives, where the set of watched pull requests is remembered between runs,
// and where every handoff to a coding agent is recorded.
//
// The directory is created on first run, along with a commented configuration
// template, so that somebody wanting to change a setting has a file to edit
// rather than a page of documentation to copy from. Nothing else is written
// until there is something to remember: a reader who never arms a pull request
// leaves no watch state and no handoff log behind.
//
// Reading it degrades rather than refuses. A file prutil cannot parse is a typo
// somebody made, or a write that was interrupted, and neither is a reason to
// take the watch feature away for the rest of the session. Load says what went
// wrong and carries on with the defaults.
package home

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// File names inside the application directory.
const (
	ConfigFile    = "config.yaml"
	StateFile     = "watch.json"
	HandoffFile   = "handoffs.jsonl"
	RepoCacheFile = "repos.json"
)

// dirPerm and filePerm keep the directory and its files private to the user.
// The configuration names repositories and local paths, and the handoff log
// records what was said to an agent, so neither is anybody else's business.
const (
	dirPerm  fs.FileMode = 0o700
	filePerm fs.FileMode = 0o600
)

// Dir resolves the application directory without creating it: PRUTIL_HOME if
// it is set, then XDG_CONFIG_HOME/prutil, then ~/.config/prutil.
func Dir() (string, error) {
	if dir := strings.TrimSpace(os.Getenv("PRUTIL_HOME")); dir != "" {
		return dir, nil
	}
	if dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); dir != "" {
		return filepath.Join(dir, "prutil"), nil
	}
	base, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not locate your home directory: %w", err)
	}
	return filepath.Join(base, ".config", "prutil"), nil
}

// Store reads and writes the application directory.
type Store struct {
	dir string
}

// Open resolves the application directory and returns a store over it. The
// directory is not created until something is written.
func Open() (*Store, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	return OpenIn(dir), nil
}

// OpenIn returns a store over an explicit directory, which is what the tests
// use.
func OpenIn(dir string) *Store { return &Store{dir: dir} }

// Dir reports the directory the store is using.
func (s *Store) Dir() string { return s.dir }

// Path is the full path of one file in the application directory.
func (s *Store) Path(name string) string { return filepath.Join(s.dir, name) }

// LoadConfig reads config.yaml. A missing file yields the defaults, because
// the feature has to work before anybody has written a line of configuration.
func (s *Store) LoadConfig() (Config, error) {
	data, err := os.ReadFile(s.Path(ConfigFile))
	if errors.Is(err, fs.ErrNotExist) {
		return DefaultConfig(), nil
	}
	if err != nil {
		return DefaultConfig(), fmt.Errorf("could not read %s: %w", s.Path(ConfigFile), err)
	}
	return ParseConfig(data)
}

// LoadOrCreateConfig reads config.yaml, creating a complete default template
// when it does not exist yet. Creation is race-safe: an existing file is never
// overwritten.
func (s *Store) LoadOrCreateConfig() (Config, error) {
	data, err := os.ReadFile(s.Path(ConfigFile))
	if err == nil {
		return ParseConfig(data)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return DefaultConfig(), fmt.Errorf("could not read %s: %w", s.Path(ConfigFile), err)
	}

	if err := os.MkdirAll(s.dir, dirPerm); err != nil {
		return DefaultConfig(), fmt.Errorf("could not create %s: %w", s.dir, err)
	}

	template := DefaultConfigTemplate()
	file, err := os.CreateTemp(s.dir, ConfigFile+".*")
	if err != nil {
		return DefaultConfig(), fmt.Errorf("could not create %s: %w", s.Path(ConfigFile), err)
	}
	tmpName := file.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if err := file.Chmod(filePerm); err != nil {
		_ = file.Close()
		return DefaultConfig(), fmt.Errorf("could not create %s: %w", s.Path(ConfigFile), err)
	}
	if _, err := file.Write(template); err != nil {
		_ = file.Close()
		return DefaultConfig(), fmt.Errorf("could not write %s: %w", s.Path(ConfigFile), err)
	}
	if err := file.Close(); err != nil {
		return DefaultConfig(), fmt.Errorf("could not write %s: %w", s.Path(ConfigFile), err)
	}
	// Link makes the fully-written temporary file visible at its final name in
	// one step and refuses to replace a configuration another process created.
	if err := os.Link(tmpName, s.Path(ConfigFile)); errors.Is(err, fs.ErrExist) {
		return s.LoadConfig()
	} else if err != nil {
		return DefaultConfig(), fmt.Errorf("could not create %s: %w", s.Path(ConfigFile), err)
	}
	return ParseConfig(template)
}

// CorruptSuffix is appended to a file prutil could not parse when it moves it
// out of the way.
const CorruptSuffix = ".corrupt"

// ErrUnreadable marks a file that is there but could not be understood, as
// against one that could not be opened at all. Only the first is worth moving
// aside: the second prutil could not move either.
var ErrUnreadable = errors.New("could not be understood")

// Startup is what one run needs from the application directory.
type Startup struct {
	Config Config
	State  *State
	// Notes are the recoverable failures. Each one is something prutil carried
	// on without, and each one is worth putting in front of the reader, since
	// the alternative is a setting they wrote being silently ignored.
	Notes []error
}

// Load reads the configuration and the watch state, creating the directory and
// a default configuration template on first run.
//
// It cannot fail. Every file it reads is optional and every one of them has a
// default, so a failure costs a note rather than the feature: a configuration
// that will not parse leaves the defaults standing, and an unreadable watch
// state starts empty. Only resolving the directory in the first place can fail,
// which is Open's business, not this method's.
func (s *Store) Load() Startup {
	out := Startup{Config: DefaultConfig(), State: NewState()}

	// A configuration is something the reader wrote on purpose, so it is never
	// moved or rewritten. Standing the defaults in its place and saying so is
	// the most prutil should do with it.
	if cfg, err := s.LoadOrCreateConfig(); err != nil {
		out.Notes = append(out.Notes, fmt.Errorf("%w; the built-in defaults are in use", err))
	} else {
		out.Config = cfg
	}

	state, err := s.LoadState()
	if err == nil {
		out.State = state
		return out
	}
	// The watch state is prutil's own bookkeeping, and starting empty means the
	// next arming overwrites it. Moving it aside first is what keeps a
	// half-written file somebody might still want to read out of the way of
	// that, rather than under it.
	note := fmt.Errorf("%w; watching starts from nothing", err)
	if errors.Is(err, ErrUnreadable) {
		if moved, mvErr := s.setAside(StateFile); mvErr != nil {
			note = errors.Join(note, mvErr)
		} else {
			note = fmt.Errorf("%w, and the old one is at %s", note, moved)
		}
	}
	out.Notes = append(out.Notes, note)
	return out
}

// setAside renames one file out of the way and reports where it went. A file
// already set aside is replaced: two unreadable versions of the same file are
// two attempts at the same thing, and the newer one is the one that explains
// what just happened.
func (s *Store) setAside(name string) (string, error) {
	target := s.Path(name + CorruptSuffix)
	if err := os.Rename(s.Path(name), target); err != nil {
		return "", fmt.Errorf("could not move %s aside: %w", s.Path(name), err)
	}
	return target, nil
}

// LoadState reads the watch state. A missing file is an empty state.
func (s *Store) LoadState() (*State, error) {
	data, err := os.ReadFile(s.Path(StateFile))
	if errors.Is(err, fs.ErrNotExist) {
		return NewState(), nil
	}
	if err != nil {
		return NewState(), fmt.Errorf("could not read %s: %w", s.Path(StateFile), err)
	}

	state := NewState()
	if err := json.Unmarshal(data, state); err != nil {
		// Wrapped as unreadable rather than unopenable, which is what tells
		// Load it is worth moving aside.
		return NewState(), fmt.Errorf("%s %w: %w", s.Path(StateFile), ErrUnreadable, err)
	}
	if state.PRs == nil {
		state.PRs = map[string]*PRState{}
	}
	return state, nil
}

// SaveState writes the watch state, replacing it atomically so that a crash
// half way through leaves the previous state intact rather than a truncated
// file the next run cannot read.
func (s *Store) SaveState(state *State) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("could not encode the watch state: %w", err)
	}
	return s.writeAtomic(StateFile, append(data, '\n'))
}

// AppendHandoff records one handoff attempt. The log is the answer to "what
// did prutil actually send, and did it land?", which a status line that has
// already scrolled away cannot give.
func (s *Store) AppendHandoff(h Handoff) error {
	data, err := json.Marshal(h)
	if err != nil {
		return fmt.Errorf("could not encode the handoff record: %w", err)
	}
	if err := os.MkdirAll(s.dir, dirPerm); err != nil {
		return fmt.Errorf("could not create %s: %w", s.dir, err)
	}

	file, err := os.OpenFile(s.Path(HandoffFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, filePerm)
	if err != nil {
		return fmt.Errorf("could not open %s: %w", s.Path(HandoffFile), err)
	}
	defer func() { _ = file.Close() }()

	if _, err := file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("could not write %s: %w", s.Path(HandoffFile), err)
	}
	return nil
}

// RecentHandoffs returns at most limit handoff attempts for pr, newest first.
// It streams the JSONL file so a long-running watcher does not load its whole
// history just to show a few entries in the detail pane.
func (s *Store) RecentHandoffs(pr string, limit int) ([]Handoff, error) {
	pr = strings.TrimSpace(pr)
	if pr == "" {
		return nil, fmt.Errorf("a pull request key is required")
	}
	if limit < 1 {
		return nil, nil
	}

	file, err := os.Open(s.Path(HandoffFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("could not open %s: %w", s.Path(HandoffFile), err)
	}
	defer func() { _ = file.Close() }()

	history := make([]Handoff, 0, limit)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		if strings.TrimSpace(scanner.Text()) == "" {
			continue
		}

		var handoff Handoff
		if err := json.Unmarshal(scanner.Bytes(), &handoff); err != nil {
			return nil, fmt.Errorf("could not read %s line %d: %w", s.Path(HandoffFile), line, err)
		}
		if handoff.PR != pr {
			continue
		}
		history = append(history, handoff)
		if len(history) > limit {
			history = history[1:]
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("could not read %s: %w", s.Path(HandoffFile), err)
	}

	slices.Reverse(history)
	return history, nil
}

// writeAtomic replaces one file by writing a sibling and renaming it over the
// target, which is atomic within a directory on every platform prutil runs on.
func (s *Store) writeAtomic(name string, data []byte) error {
	if err := os.MkdirAll(s.dir, dirPerm); err != nil {
		return fmt.Errorf("could not create %s: %w", s.dir, err)
	}

	tmp, err := os.CreateTemp(s.dir, name+".*")
	if err != nil {
		return fmt.Errorf("could not write %s: %w", s.Path(name), err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	if err := tmp.Chmod(filePerm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("could not write %s: %w", s.Path(name), err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("could not write %s: %w", s.Path(name), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("could not write %s: %w", s.Path(name), err)
	}
	if err := os.Rename(tmp.Name(), s.Path(name)); err != nil {
		return fmt.Errorf("could not write %s: %w", s.Path(name), err)
	}
	return nil
}

// Handoff is one line of the handoff log.
type Handoff struct {
	At      time.Time `json:"at"`
	PR      string    `json:"pr"`
	URL     string    `json:"url"`
	Outcome string    `json:"outcome"`
	// Target is the herdr pane the prompt was addressed to, and Kind the agent
	// herdr detected there. Both are empty when no agent was found.
	Target string `json:"target,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Dir    string `json:"dir,omitempty"`
	// Provisioned distinguishes an agent prutil created for this handoff from
	// one that was already running in a local checkout.
	Provisioned bool   `json:"provisioned,omitempty"`
	Workspace   string `json:"workspace,omitempty"`
	Tab         string `json:"tab,omitempty"`
	Detail      string `json:"detail,omitempty"`
	Prompt      string `json:"prompt,omitempty"`
}

// Handoff outcomes, as recorded in the log.
const (
	OutcomeSent    = "sent"
	OutcomeDryRun  = "dry-run"
	OutcomeNoAgent = "no-agent"
	OutcomeBlocked = "blocked"
	OutcomeFailed  = "failed"
)
