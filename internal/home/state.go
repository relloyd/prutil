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
	LastHandoff          time.Time `json:"last_handoff,omitempty"`
	LastCheckHandoffHead string    `json:"last_check_handoff_head,omitempty"`
	// NotifiedThreads maps each unresolved review thread prutil has handed off
	// to the id of the newest comment it held at the time. A thread already in
	// here is not new; a thread in here whose newest comment has changed has
	// been replied to, which is new again.
	NotifiedThreads map[string]string `json:"notified_threads,omitempty"`
	// Adopted is set on a pull request somebody else opened that the reader has
	// taken on. See Adoption.
	Adopted *Adoption `json:"adopted,omitempty"`
	// AcceptedAgentReplies are the comment ids of other agents' replies the
	// reader has seen and chose to watch the pull request regardless of. A
	// reply in here does not stop the watch again; a new one does.
	AcceptedAgentReplies []string `json:"accepted_agent_replies,omitempty"`
}

// Adoption is the reader's decision to work on a pull request somebody else
// opened: it is listed beside their own, and its author is trusted on it.
//
// The grant is to Author on this pull request alone, as the reader saw it when
// they adopted it. It is recorded rather than read from the pull request each
// time because the reader agreed to a person, and a pull request whose author
// GitHub has since lost, or renamed, is not the one they agreed to.
type Adoption struct {
	// NodeID is how the pull request is read back on every load, since no
	// search of the reader's own pull requests finds it.
	NodeID string `json:"node_id"`
	// Author is the login the reader agreed to trust on this pull request.
	Author string `json:"author"`
	// At is when it was adopted.
	At time.Time `json:"at"`
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
	entry := s.Mutate(key)
	entry.Armed = armed
	if !armed {
		entry.LastCheckHandoffHead = ""
		entry.AcceptedAgentReplies = nil
	}
	return armed
}

// ToggleArmed flips a pull request between armed and disarmed and reports the
// new setting.
func (s *State) ToggleArmed(key string) bool {
	entry := s.Mutate(key)
	entry.Armed = !entry.Armed
	if !entry.Armed {
		entry.LastCheckHandoffHead = ""
		entry.AcceptedAgentReplies = nil
	}
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

// Adopt records that the reader has taken on a pull request somebody else
// opened. It does not arm it: watching another author's pull request is a
// second decision, made with its own key press.
func (s *State) Adopt(key string, adoption Adoption) {
	s.Mutate(key).Adopted = &adoption
}

// Release forgets an adoption and everything that only made sense while it
// stood: the watch, the replies accepted under it, and the threads already
// handed over, which would otherwise keep the entry in the file for good.
// It reports whether the pull request was adopted.
func (s *State) Release(key string) bool {
	if s == nil || s.PRs[key] == nil || s.PRs[key].Adopted == nil {
		return false
	}
	entry := s.PRs[key]
	entry.Adopted = nil
	entry.AcceptedAgentReplies = nil
	entry.NotifiedThreads, entry.LastHandoff = nil, time.Time{}
	entry.Armed, entry.LastCheckHandoffHead = false, ""
	return true
}

// Adoption returns the adoption recorded for a pull request, reporting false
// for one of the reader's own or one they have released.
func (s *State) Adoption(key string) (Adoption, bool) {
	got := s.Get(key).Adopted
	if got == nil {
		return Adoption{}, false
	}
	return *got, true
}

// AdoptedKeys lists every adopted pull request, in the same order ArmedKeys
// uses.
func (s *State) AdoptedKeys() []string {
	if s == nil {
		return nil
	}
	keys := make([]string, 0, len(s.PRs))
	for key, entry := range s.PRs {
		if entry != nil && entry.Adopted != nil {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

// Compact drops entries that hold nothing worth keeping, which is what stops
// the file growing a line for every pull request the reader ever looked at.
func (s *State) Compact() {
	for key, entry := range s.PRs {
		if entry == nil || (!entry.Armed && len(entry.NotifiedThreads) == 0 && entry.Adopted == nil) {
			delete(s.PRs, key)
		}
	}
}
