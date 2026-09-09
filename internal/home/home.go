// Package home owns prutil's application directory: where its configuration
// lives, where the set of watched pull requests is remembered between runs,
// and where every handoff to a coding agent is recorded.
//
// Nothing in prutil requires the directory to exist. A reader who never arms a
// pull request never causes a file to be written, and a missing configuration
// file is not an error, only a request for the defaults.
package home

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// File names inside the application directory.
const (
	ConfigFile  = "config.yaml"
	StateFile   = "watch.json"
	HandoffFile = "handoffs.jsonl"
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
		return NewState(), fmt.Errorf("could not read %s: %w", s.Path(StateFile), err)
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
	Detail string `json:"detail,omitempty"`
	Prompt string `json:"prompt,omitempty"`
}

// Handoff outcomes, as recorded in the log.
const (
	OutcomeSent    = "sent"
	OutcomeDryRun  = "dry-run"
	OutcomeNoAgent = "no-agent"
	OutcomeBlocked = "blocked"
	OutcomeFailed  = "failed"
)
