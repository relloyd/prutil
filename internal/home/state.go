package home

import (
	"sort"
	"time"
)

// State is what prutil remembers between runs: which pull requests are armed
// for watching, and which review threads it has already handed to an agent.
//
// Pull requests are keyed the way GitHub writes them, "owner/name#12", so the
// file can be read and edited by hand.
type State struct {
	PRs map[string]*PRState `json:"prs"`
}

// PRState is what is remembered about one pull request.
type PRState struct {
	// Armed is the reader's decision that this pull request's feedback is
	// worth acting on. It is deliberately per pull request: a review whose
	// remaining comments will never be resolved should not cost anything.
	Armed bool `json:"armed"`
	// LastHandoff is when prutil last sent this pull request to an agent.
	LastHandoff time.Time `json:"last_handoff,omitempty"`
	// NotifiedThreads maps each unresolved review thread prutil has handed off
	// to the id of the newest comment it held at the time. A thread already in
	// here is not new; a thread in here whose newest comment has changed has
	// been replied to, which is new again.
	NotifiedThreads map[string]string `json:"notified_threads,omitempty"`
}

// NewState returns an empty state.
func NewState() *State {
	return &State{PRs: map[string]*PRState{}}
}

// Get returns what is remembered about one pull request, or the zero value
// when nothing is. The result is a copy: use Mutate to change anything.
func (s *State) Get(key string) PRState {
	if s == nil || s.PRs == nil {
		return PRState{}
	}
	if got, ok := s.PRs[key]; ok && got != nil {
		return *got
	}
	return PRState{}
}

// Mutate returns the entry for one pull request, creating it if it is new.
func (s *State) Mutate(key string) *PRState {
	if s.PRs == nil {
		s.PRs = map[string]*PRState{}
	}
	if got, ok := s.PRs[key]; ok && got != nil {
		return got
	}
	entry := &PRState{}
	s.PRs[key] = entry
	return entry
}

// Armed reports whether a pull request is being watched.
func (s *State) Armed(key string) bool { return s.Get(key).Armed }

// SetArmed arms or disarms a pull request and reports the new setting.
func (s *State) SetArmed(key string, armed bool) bool {
	s.Mutate(key).Armed = armed
	return armed
}

// ToggleArmed flips a pull request between armed and disarmed and reports the
// new setting.
func (s *State) ToggleArmed(key string) bool {
	entry := s.Mutate(key)
	entry.Armed = !entry.Armed
	return entry.Armed
}

// ArmedKeys lists every armed pull request, in GitHub's own order so that the
// file and any message built from it read the same way twice.
func (s *State) ArmedKeys() []string {
	if s == nil {
		return nil
	}
	keys := make([]string, 0, len(s.PRs))
	for key, entry := range s.PRs {
		if entry != nil && entry.Armed {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

// ArmedCount is how many pull requests are being watched.
func (s *State) ArmedCount() int { return len(s.ArmedKeys()) }

// RecordHandoff marks every thread in unresolved as handed off, so that the
// same feedback is never sent to an agent twice. threads maps a review thread
// id to the id of its newest comment.
func (s *State) RecordHandoff(key string, threads map[string]string, at time.Time) {
	entry := s.Mutate(key)
	entry.LastHandoff = at
	if entry.NotifiedThreads == nil {
		entry.NotifiedThreads = map[string]string{}
	}
	for id, newest := range threads {
		entry.NotifiedThreads[id] = newest
	}
}

// Compact drops entries that hold nothing worth keeping, which is what stops
// the file growing a line for every pull request the reader ever looked at.
func (s *State) Compact() {
	for key, entry := range s.PRs {
		if entry == nil || (!entry.Armed && len(entry.NotifiedThreads) == 0) {
			delete(s.PRs, key)
		}
	}
}
