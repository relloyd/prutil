package model

import (
	"strings"
	"time"
)

// SelfTestMarker opts an unresolved code-review thread into watcher feedback
// when the authenticated viewer placed it in the opening comment or their
// latest reply. It is an HTML comment so the marker does not add visible
// noise to a pull request's rendered discussion.
const SelfTestMarker = "<!-- prutil:test -->"

// ReviewThread is one conversation attached to a pull request, as GitHub's
// review UI groups them: a first comment on a line of the diff and every reply
// under it, resolved or not.
//
// prutil cares about threads rather than comment counts because only a thread
// knows whether it has been dealt with. A pull request's issue-comment total
// moves for chatter that needs no work and stays still for a review comment
// that needs plenty.
type ReviewThread struct {
	ID string
	// Resolved is the reviewer's own mark that the thread is finished with.
	Resolved bool
	// Outdated means the lines the thread was left on have since changed. Such
	// a thread is still feedback: the change may or may not be the answer to it.
	Outdated bool
	// Path is the file the thread hangs off, empty for a thread GitHub could
	// not place.
	Path string
	// URL points at the thread's newest comment, which is what somebody
	// following the link wants to read first.
	URL string

	// Opener is the login that started the thread and Body its opening
	// comment, which together are the context an agent needs.
	Opener   string
	OpenedAt time.Time
	Body     string

	// LatestBy is the login that spoke in the thread last, LatestID that
	// comment's identity and LatestAt its time. A thread whose newest comment
	// is the pull request author's own is a thread they have already answered.
	LatestBy   string
	LatestID   string
	LatestAt   time.Time
	LatestBody string

	// Comments is how many comments the thread holds.
	Comments int
}

// NeedsAttention reports whether a thread is feedback still waiting on viewer.
// A resolved thread is finished, and a thread whose last word is the viewer's
// own has already been answered by them unless its self-authored opening
// comment explicitly carries SelfTestMarker.
func (t ReviewThread) NeedsAttention(viewer string) bool {
	if t.Resolved {
		return false
	}
	return viewer == "" ||
		!strings.EqualFold(t.LatestBy, viewer) ||
		t.selfTestComment(viewer)
}

// selfTestComment recognises an explicit watcher test only when the current
// viewer wrote the marked comment. A marker from another reviewer must not
// change the ordinary last-author rule.
func (t ReviewThread) selfTestComment(viewer string) bool {
	if viewer == "" {
		return false
	}
	return (strings.EqualFold(t.Opener, viewer) && strings.Contains(t.Body, SelfTestMarker)) ||
		(strings.EqualFold(t.LatestBy, viewer) && strings.Contains(t.LatestBody, SelfTestMarker))
}

// Feedback selects the threads still waiting on viewer, keeping the order they
// arrived in.
func Feedback(threads []ReviewThread, viewer string) []ReviewThread {
	out := make([]ReviewThread, 0, len(threads))
	for _, thread := range threads {
		if thread.NeedsAttention(viewer) {
			out = append(out, thread)
		}
	}
	return out
}

// Unhandled selects the threads that have not already been handed to an agent.
// notified maps a thread id to the newest comment it held when it was handed
// over, so a thread that has since been replied to counts as new again.
func Unhandled(threads []ReviewThread, notified map[string]string) []ReviewThread {
	out := make([]ReviewThread, 0, len(threads))
	for _, thread := range threads {
		if seen, ok := notified[thread.ID]; ok && seen == thread.LatestID {
			continue
		}
		out = append(out, thread)
	}
	return out
}

// Digest reduces threads to the thread-id to newest-comment-id mapping the
// watch state remembers.
func Digest(threads []ReviewThread) map[string]string {
	out := make(map[string]string, len(threads))
	for _, thread := range threads {
		out[thread.ID] = thread.LatestID
	}
	return out
}
