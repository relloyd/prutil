package model

import (
	"strings"
	"time"
)

// DefaultSelfTestMarker opts an unresolved code-review thread into watcher
// feedback when the authenticated viewer placed it in the thread's newest
// comment. It is an HTML comment so the marker adds no visible noise to a pull
// request's rendered discussion.
//
// It answers for the newest comment alone, so that a thread the agent or the
// viewer has since replied to falls off the list rather than being handed over
// for as long as it stays open. It only ever answers for a comment the viewer
// wrote themselves, so nobody else can use it to change what a reader's
// watcher does. It exists so that the watcher can be tried against a real pull
// request without waiting for somebody to review it. The marker is
// configurable, and setting it to the empty string turns the behaviour off;
// see home.WatchConfig.
const DefaultSelfTestMarker = "<!-- prutil:test -->"

// AgentCommentMarker identifies a review reply an agent posted on the viewer's
// behalf, so that answering feedback does not read as fresh feedback and send
// the same work round again. It is an HTML comment, so it adds no visible noise
// to a pull request's rendered discussion, and the agent is told to write it by
// the prompt in home.DefaultPrompt.
const AgentCommentMarker = "<!-- prutil:agent -->"

// IsAgentComment reports whether a comment carries the tracking tag an agent
// writing on the viewer's behalf leaves behind.
//
// Only the tag answers. Prose such as "automated response" is something a
// reviewer can type, and matching on it would let an ordinary comment take
// itself off the feedback list by accident.
func IsAgentComment(body string) bool {
	return strings.Contains(body, AgentCommentMarker)
}

// Participant is one account that has spoken in a review thread, in the terms
// a trust policy asks about: who they are, what GitHub says their relationship
// to the repository is, and whether they are an app rather than a person.
type Participant struct {
	// Login is the account name, empty for a comment GitHub could not
	// attribute because the account has since been deleted. GraphQL reports a
	// bot's login without the [bot] suffix that REST uses.
	Login string
	// Association is GitHub's authorAssociation for the comment: OWNER,
	// MEMBER, COLLABORATOR, CONTRIBUTOR, NONE and the rest.
	Association string
	// Bot distinguishes a GitHub App from a person who registered the same
	// name as their login.
	Bot bool
}

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
	// LatestPending is true when GitHub has not published the newest comment
	// from its review draft yet.
	LatestPending bool

	// Comments is how many comments the thread holds.
	Comments int

	// Participants is everyone who has spoken in the thread. The first and the
	// last comment are not enough to decide whether a thread can be trusted:
	// an attacker can reply in the middle of one, and a trusted reviewer can
	// reply after them.
	Participants []Participant
	// ParticipantsComplete is false when GitHub returned fewer participants
	// than the thread holds comments, so who spoke in it is not fully known.
	// Not knowing who spoke is not the same as knowing.
	ParticipantsComplete bool
}

// ReviewFilter configures how review threads are filtered for watcher feedback.
type ReviewFilter struct {
	// Viewer is the authenticated login name of the viewer.
	Viewer string
	// Marker is the optional HTML comment marker for self-test comments.
	Marker string
	// SelfReview treats all unresolved review comments written by the viewer
	// as actionable feedback, as long as they are not automated agent comments.
	SelfReview bool
}

// NeedsAttention reports whether a thread is feedback still waiting on viewer.
// A resolved thread is finished. A thread whose newest comment is an agent
// reply of the viewer's own has already been answered. A thread whose last word
// is the viewer's own is otherwise answered unless filter.SelfReview is enabled
// or their latest comment carries filter.Marker.
func (t ReviewThread) NeedsAttention(filter ReviewFilter) bool {
	if t.Resolved {
		return false
	}
	if t.LatestPending {
		return false
	}
	if t.agentAnswered(filter.Viewer) {
		return false
	}
	if filter.Viewer == "" || !strings.EqualFold(t.LatestBy, filter.Viewer) {
		return true
	}
	if filter.SelfReview {
		return true
	}
	return t.selfTestComment(filter.Viewer, filter.Marker)
}

// agentAnswered reports whether the thread's newest comment is an agent reply
// posted under the viewer's own account.
//
// The author check is what keeps the marker from being a thing somebody else
// can do to a reader's watcher: a reviewer writing it, deliberately or by
// quoting an agent that did, must not take their own feedback off the list.
func (t ReviewThread) agentAnswered(viewer string) bool {
	if viewer == "" {
		return false
	}
	latest, by := t.LatestBody, t.LatestBy
	if latest == "" {
		latest, by = t.Body, t.Opener
	}
	return strings.EqualFold(by, viewer) && IsAgentComment(latest)
}

// selfTestComment recognises an explicit watcher test only when the current
// viewer wrote the latest comment carrying marker. Checking only the latest
// comment prevents a thread from remaining permanently open once an agent or
// the viewer replies.
func (t ReviewThread) selfTestComment(viewer, marker string) bool {
	if viewer == "" || marker == "" {
		return false
	}
	if !strings.EqualFold(t.LatestBy, viewer) {
		return false
	}
	if strings.Contains(t.LatestBody, marker) {
		return true
	}
	if t.LatestBody == "" && strings.EqualFold(t.Opener, viewer) && strings.Contains(t.Body, marker) {
		return true
	}
	return false
}

// TrustPolicy decides whose review feedback may be handed to an agent without
// the reader being asked first.
//
// The question it answers is not the same as the one ReviewFilter answers. A
// thread can be feedback and untrusted, or trusted and already dealt with.
// Keeping them apart is deliberate: trust that filtered threads instead of
// holding the handoff would take held feedback out of the counts on screen,
// and the reader would be told a pull request was quiet when it was not.
type TrustPolicy struct {
	// Viewer is the authenticated login, who is always trusted. An empty
	// viewer trusts nobody by that route rather than everybody.
	Viewer string
	// Associations are the GitHub authorAssociation values that carry trust,
	// such as OWNER and COLLABORATOR.
	Associations []string
	// Authors are extra logins that carry trust. An entry ending in [bot]
	// matches only a GitHub App.
	Authors []string
	// Marker is prutil's own self-test HTML comment, which the hidden-content
	// detector has to recognise so that it does not flag the reader's own way
	// of exercising the watcher.
	Marker string
}

// trusts reports whether one participant's word may reach an agent unasked.
func (p TrustPolicy) trusts(who Participant) bool {
	if who.Login == "" {
		return false
	}
	if p.Viewer != "" && strings.EqualFold(who.Login, p.Viewer) {
		return true
	}
	for _, association := range p.Associations {
		if who.Association != "" && strings.EqualFold(association, who.Association) {
			return true
		}
	}
	for _, author := range p.Authors {
		if namesParticipant(author, who) {
			return true
		}
	}
	return false
}

// namesParticipant applies the [bot] suffix rule to one configured author. An
// entry naming a GitHub App matches only an App, and every other entry matches
// only a person, because GraphQL reports a bot's login without the suffix REST
// uses: without the rule, somebody who registered a bot's name as their own
// login would inherit its trust.
func namesParticipant(entry string, who Participant) bool {
	name, bot := strings.CutSuffix(strings.TrimSpace(entry), "[bot]")
	if bot != who.Bot {
		return false
	}
	return name != "" && strings.EqualFold(name, who.Login)
}

// DeletedAccount stands in for an author GitHub no longer has, so that a hold
// can name what caused it without an empty string in the list.
const DeletedAccount = "a deleted account"

// Hold is why a pull request's feedback is not being handed over on its own.
// The zero value holds nothing.
type Hold struct {
	// Authors names the untrusted participants, in the order first met.
	Authors []string
	// Hidden names whoever wrote a comment carrying text github.com does not
	// show. It is kept apart from Authors because a trusted author's account
	// can be the compromised one, which is the case this catches.
	Hidden []string
	// Unknown is true when a thread came back with fewer participants than it
	// holds comments, so who spoke in it could not be established at all.
	Unknown bool
}

// Held reports whether anything about the pull request stops an automatic
// handoff.
func (h Hold) Held() bool {
	return len(h.Authors) > 0 || len(h.Hidden) > 0 || h.Unknown
}

// HoldFor asks both questions about every unresolved thread on a pull request:
// whether anyone outside the trust boundary has spoken in it, and whether any
// comment in it carries text the reader cannot see.
//
// It reads every unresolved thread rather than the ones Feedback returns. A
// thread whose last word is the viewer's own is not feedback, but an agent
// handed the pull request reads it along with the rest, so a comment sitting
// in it counts. It is also what makes the hold releasable: resolving a hostile
// thread on GitHub takes it out of this list, and the next poll goes through.
func HoldFor(threads []ReviewThread, policy TrustPolicy) Hold {
	hold := untrusted(threads, policy)
	hold.Hidden = hiddenAuthors(threads, policy)
	return hold
}

// untrusted names everyone outside the boundary who has spoken, and reports
// whether any thread's participants could not be established at all.
func untrusted(threads []ReviewThread, policy TrustPolicy) Hold {
	var hold Hold
	seen := make(map[string]bool)
	for _, thread := range threads {
		if thread.Resolved {
			continue
		}
		if !thread.ParticipantsComplete {
			hold.Unknown = true
		}
		for _, who := range thread.Participants {
			if policy.trusts(who) {
				continue
			}
			hold.Authors = appendName(hold.Authors, seen, who.Login)
		}
	}
	return hold
}

// hiddenAuthors names everyone whose comment on an unresolved thread carries
// text github.com does not render.
//
// prutil holds the pull request rather than stripping the text, because the
// agent does not read the body prutil fetched: it is told to go and read the
// threads itself, from the same API, and would find whatever was hidden there
// still in place.
func hiddenAuthors(threads []ReviewThread, policy TrustPolicy) []string {
	var out []string
	seen := make(map[string]bool)
	for _, thread := range threads {
		if thread.Resolved {
			continue
		}
		if thread.hides(thread.Opener, thread.Body, policy) {
			out = appendName(out, seen, thread.Opener)
		}
		if thread.LatestBody != "" && thread.hides(thread.LatestBy, thread.LatestBody, policy) {
			out = appendName(out, seen, thread.LatestBy)
		}
	}
	return out
}

// appendName adds a login to a hold's list once, however it is capitalised,
// standing in for one GitHub has lost.
func appendName(names []string, seen map[string]bool, login string) []string {
	if login == "" {
		login = DeletedAccount
	}
	key := strings.ToLower(login)
	if seen[key] {
		return names
	}
	seen[key] = true
	return append(names, login)
}

// hides reports whether one comment in this thread carries text the reader
// cannot see.
func (t ReviewThread) hides(login, body string, policy TrustPolicy) bool {
	if hiddenRunes(body) {
		return true
	}
	// An HTML comment is how review bots carry their metadata and how prutil
	// carries its own markers, so it only counts in somebody else's prose.
	// Invisible characters count in anybody's, the viewer's included: an
	// account that has been taken is still the account it was.
	if login != "" && (strings.EqualFold(login, policy.Viewer) || t.spokeAsBot(login)) {
		return false
	}
	return hiddenComment(body, policy.Marker)
}

// spokeAsBot reports whether the named login is a GitHub App in this thread.
func (t ReviewThread) spokeAsBot(login string) bool {
	for _, who := range t.Participants {
		if who.Bot && who.Login != "" && strings.EqualFold(who.Login, login) {
			return true
		}
	}
	return false
}

// hiddenComment reports whether body carries an HTML comment that is not one
// of prutil's own markers. github.com renders none of them.
func hiddenComment(body, marker string) bool {
	body = strings.ReplaceAll(body, AgentCommentMarker, "")
	if marker != "" {
		body = strings.ReplaceAll(body, marker, "")
	}
	return strings.Contains(body, "<!--")
}

// hiddenRunes reports whether body carries characters github.com does not
// render, which a review comment has no ordinary reason to contain.
//
// The ranges are the ones that carry text invisibly: tag characters, which
// encode ASCII one to one; the zero-width and format characters; and the bidi
// controls, which can reorder a line so that what is displayed is not what is
// written.
func hiddenRunes(body string) bool {
	runes := []rune(body)
	for i, r := range runes {
		switch {
		case r >= 0xE0000 && r <= 0xE007F:
			return true
		case r == 0x200D:
			// The zero-width joiner is how every family and profession emoji
			// is built, so it only counts away from one. Two in a row are not
			// an emoji and do count: a run of joiners between words encodes
			// data as readily as a tag character does.
			if !joinsEmoji(runes, i) {
				return true
			}
		case r >= 0x200B && r <= 0x200F, r >= 0x2060 && r <= 0x2064, r == 0xFEFF:
			return true
		case r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069:
			return true
		}
	}
	return false
}

// joinsEmoji reports whether the joiner at i sits between two characters an
// emoji sequence is built from.
func joinsEmoji(runes []rune, i int) bool {
	return i > 0 && i+1 < len(runes) &&
		emojiPart(runes[i-1]) && emojiPart(runes[i+1])
}

// emojiPart reports whether r can take part in an emoji joined sequence: a
// pictograph, a regional indicator, a skin-tone modifier, a dingbat, or the
// variation selector that turns a plain symbol into one — 🏳️‍🌈 joins on the
// selector rather than on the flag.
//
// It is deliberately generous. Being wrong here the other way would hold a
// pull request over somebody's ordinary comment, and the rules that catch a
// payload outright — the tag characters and the bidi controls — do not depend
// on it.
func emojiPart(r rune) bool {
	switch {
	case r >= 0x1F000 && r <= 0x1FAFF:
		return true
	case r >= 0x2600 && r <= 0x27BF:
		return true
	case r >= 0x2B00 && r <= 0x2BFF:
		return true
	case r >= 0x2190 && r <= 0x21FF:
		return true
	case r == 0xFE0E || r == 0xFE0F:
		return true
	}
	return false
}

// Feedback selects the threads still waiting on viewer, keeping the order they
// arrived in.
func Feedback(threads []ReviewThread, filter ReviewFilter) []ReviewThread {
	out := make([]ReviewThread, 0, len(threads))
	for _, thread := range threads {
		if thread.NeedsAttention(filter) {
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
