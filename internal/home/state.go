package home

import (
	"sort"
	"strings"
	"time"
)

// State is what prutil remembers between runs: which pull requests are armed
// for watching, and which review threads it has already handed to an agent.
//
// Pull requests are keyed the way GitHub writes them, "owner/name#12", so the
// file can be read and edited by hand.
type State struct {
	PRs map[string]*PRState `json:"prs"`
	// Repos are the repositories the reader has browsed or adopted from in the
	// adopt pane, most recent first, so that the next time they are one key
	// press away rather than a name to type.
	Repos []RecentRepo `json:"recent_repos,omitempty"`
	// AutoWatch is what watch.auto_watch has decided so far, nil while it is
	// off.
	AutoWatch *AutoWatch `json:"auto_watch,omitempty"`
}

// AutoWatch is the record that keeps automatic arming to new pull requests,
// and to each of them once.
type AutoWatch struct {
	// Since is when it was switched on. Only a pull request created after it
	// is new; everything open before is left as the reader had it.
	Since time.Time `json:"since"`
	// Considered holds each pull request already armed automatically, keyed
	// like PRs, with when it was created. Being in here is what stops a pull
	// request the reader has since stopped watching from being armed again.
	Considered map[string]time.Time `json:"considered,omitempty"`
}

// StartAutoWatch begins automatic arming from at, forgetting anything an
// earlier run of it decided: a pull request opened while it was off is not
// new by the time it is switched back on.
func (s *State) StartAutoWatch(at time.Time) {
	s.AutoWatch = &AutoWatch{Since: at, Considered: map[string]time.Time{}}
}

// StopAutoWatch forgets automatic arming. What it armed stays armed.
func (s *State) StopAutoWatch() {
	s.AutoWatch = nil
}

// Consider records that a pull request has been armed automatically, so that
// it never is again.
func (w *AutoWatch) Consider(key string, created time.Time) {
	if w.Considered == nil {
		w.Considered = map[string]time.Time{}
	}
	w.Considered[key] = created
}

// Seen reports whether a pull request has been armed automatically
// before.
func (w *AutoWatch) Seen(key string) bool {
	_, ok := w.Considered[key]
	return ok
}

// Forget drops every considered pull request that is not in open, which must
// be every open pull request the reader has: GitHub does not reuse a number,
// so one that has closed will never be new again, and dropping it is what
// keeps the record the size of the open list.
func (w *AutoWatch) Forget(open map[string]bool) {
	for key := range w.Considered {
		if !open[key] {
			delete(w.Considered, key)
		}
	}
}

// RecentRepo is one repository the reader has used in the adopt pane.
type RecentRepo struct {
	Repo string    `json:"repo"`
	At   time.Time `json:"at"`
}

// recentRepoLimit caps the recent repositories. It is a shortlist, and the
// pane adds every repository the lists already hold beneath it.
const recentRepoLimit = 20

// TouchRepo moves a repository to the front of the recent ones, adding it when
// it is new and dropping the oldest past the cap.
func (s *State) TouchRepo(repo string, at time.Time) {
	repos := make([]RecentRepo, 0, len(s.Repos)+1)
	repos = append(repos, RecentRepo{Repo: repo, At: at})
	for _, got := range s.Repos {
		if !strings.EqualFold(got.Repo, repo) {
			repos = append(repos, got)
		}
	}
	s.Repos = repos[:min(len(repos), recentRepoLimit)]
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
	// PostOnPass is the reader's decision that the checks_passed comment is
	// posted on this pull request each time its checks pass. It answers only
	// while the pull request is armed, and disarming clears it: the watcher's
	// reading of the check rollup is what it acts on.
	PostOnPass bool `json:"post_on_pass,omitempty"`
	// LastPassCommentHead is the head commit the comment was last posted for,
	// so that it is posted once per commit rather than on every poll that
	// finds the checks still green.
	LastPassCommentHead string `json:"last_pass_comment_head,omitempty"`
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
		entry.disarm()
	}
	return armed
}

// ToggleArmed flips a pull request between armed and disarmed and reports the
// new setting.
func (s *State) ToggleArmed(key string) bool {
	entry := s.Mutate(key)
	entry.Armed = !entry.Armed
	if !entry.Armed {
		entry.disarm()
	}
	return entry.Armed
}

// disarm forgets what only made sense while the pull request was watched.
func (e *PRState) disarm() {
	e.LastCheckHandoffHead = ""
	e.AcceptedAgentReplies = nil
	e.PostOnPass, e.LastPassCommentHead = false, ""
}

// TogglePostOnPass flips whether the checks_passed comment is posted on an
// armed pull request and reports the new setting. A pull request that is not
// armed cannot have it, so it reports false and changes nothing.
func (s *State) TogglePostOnPass(key string) bool {
	if !s.Armed(key) {
		return false
	}
	entry := s.Mutate(key)
	entry.PostOnPass = !entry.PostOnPass
	entry.LastPassCommentHead = ""
	return entry.PostOnPass
}

// PostOnPass reports whether the checks_passed comment is due on a pull
// request whenever its checks pass.
func (s *State) PostOnPass(key string) bool {
	got := s.Get(key)
	return got.Armed && got.PostOnPass
}

// RecordPassComment marks the checks_passed comment as posted for head.
func (s *State) RecordPassComment(key, head string) {
	s.Mutate(key).LastPassCommentHead = head
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
	entry.Armed = false
	entry.disarm()
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
