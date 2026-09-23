package model_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/relloyd/prutil/internal/model"
)

func TestParseRollupState(t *testing.T) {
	cases := map[string]model.Status{
		"SUCCESS":  model.StatusSuccess,
		"FAILURE":  model.StatusFailure,
		"ERROR":    model.StatusFailure,
		"PENDING":  model.StatusPending,
		"EXPECTED": model.StatusPending,
		"":         model.StatusUnknown,
		"nonsense": model.StatusUnknown,
		"success":  model.StatusSuccess,
	}
	for state, want := range cases {
		t.Run(state, func(t *testing.T) {
			assert.Equal(t, want, model.ParseRollupState(state))
		})
	}
}

func TestParseCheckRun(t *testing.T) {
	cases := []struct {
		name       string
		status     string
		conclusion string
		want       model.Status
	}{
		{"queued run is pending", "QUEUED", "", model.StatusPending},
		{"running run is pending", "IN_PROGRESS", "", model.StatusPending},
		{"running run ignores a stale conclusion", "IN_PROGRESS", "SUCCESS", model.StatusPending},
		{"completed success", "COMPLETED", "SUCCESS", model.StatusSuccess},
		{"completed failure", "COMPLETED", "FAILURE", model.StatusFailure},
		{"timed out counts as failure", "COMPLETED", "TIMED_OUT", model.StatusFailure},
		{"action required counts as failure", "COMPLETED", "ACTION_REQUIRED", model.StatusFailure},
		{"cancelled", "COMPLETED", "CANCELLED", model.StatusCancelled},
		{"neutral", "COMPLETED", "NEUTRAL", model.StatusNeutral},
		{"skipped", "COMPLETED", "SKIPPED", model.StatusSkipped},
		{"unrecognised", "COMPLETED", "SOMETHING_NEW", model.StatusUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, model.ParseCheckRun(c.status, c.conclusion))
		})
	}
}

func TestParseMergeableAndReviewDecision(t *testing.T) {
	assert.Equal(t, model.MergeClean, model.ParseMergeable("MERGEABLE"))
	assert.Equal(t, model.MergeConflicting, model.ParseMergeable("CONFLICTING"))
	assert.Equal(t, model.MergeUnknown, model.ParseMergeable("UNKNOWN"))

	assert.Equal(t, model.ReviewApproved, model.ParseReviewDecision("APPROVED"))
	assert.Equal(t, model.ReviewChangesRequested, model.ParseReviewDecision("CHANGES_REQUESTED"))
	assert.Equal(t, model.ReviewRequired, model.ParseReviewDecision("REVIEW_REQUIRED"))
	assert.Equal(t, model.ReviewNone, model.ParseReviewDecision(""))

	assert.Equal(t, "APPROVED", model.ReviewApproved.String())
	assert.Empty(t, model.ReviewNone.String(), "no decision means no badge")
}

func TestCountChecksAndRollup(t *testing.T) {
	checks := []model.Check{
		{Status: model.StatusSuccess},
		{Status: model.StatusSuccess},
		{Status: model.StatusFailure},
		{Status: model.StatusPending},
		{Status: model.StatusSkipped},
	}

	counts := model.CountChecks(checks)
	assert.Equal(t, model.CheckCounts{Success: 2, Failure: 1, Pending: 1, Other: 1, Total: 5}, counts)
	assert.Equal(t, model.StatusFailure, counts.Rollup(), "a failure outranks everything")

	assert.Equal(t, model.StatusPending, model.CountChecks([]model.Check{
		{Status: model.StatusSuccess},
		{Status: model.StatusPending},
	}).Rollup())

	assert.Equal(t, model.StatusSuccess, model.CountChecks([]model.Check{
		{Status: model.StatusSuccess},
	}).Rollup())

	assert.Equal(t, model.StatusUnknown, model.CountChecks(nil).Rollup())

	assert.Equal(t, model.StatusNeutral, model.CountChecks([]model.Check{
		{Status: model.StatusSkipped},
	}).Rollup(), "skipped-only checks are neither good nor bad")
}

func TestSortByCreatedDescIsStable(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	prs := []model.PullRequest{
		{Repo: "b/one", Number: 1, CreatedAt: now.Add(-48 * time.Hour)},
		{Repo: "a/two", Number: 9, CreatedAt: now},
		{Repo: "a/two", Number: 2, CreatedAt: now},
		{Repo: "c/three", Number: 3, CreatedAt: now.Add(-time.Hour)},
	}

	model.SortByCreatedDesc(prs)

	assert.Equal(t, []int{2, 9, 3, 1}, []int{prs[0].Number, prs[1].Number, prs[2].Number, prs[3].Number},
		"newest first, ties broken by repo then number")
}

func TestHumanAge(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{-time.Hour, "just now"},
		{30 * time.Second, "just now"},
		{45 * time.Minute, "45m"},
		{6 * time.Hour, "6h"},
		{23*time.Hour + 59*time.Minute, "23h"},
		{3 * 24 * time.Hour, "3d"},
		{59 * 24 * time.Hour, "59d"},
		{90 * 24 * time.Hour, "3mo"},
		{400 * 24 * time.Hour, "1y"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			assert.Equal(t, c.want, model.HumanAge(c.in))
		})
	}
}

func TestHumanDuration(t *testing.T) {
	assert.Empty(t, model.HumanDuration(0))
	assert.Equal(t, "12s", model.HumanDuration(12*time.Second))
	assert.Equal(t, "1m20s", model.HumanDuration(80*time.Second))
	assert.Equal(t, "1h5m", model.HumanDuration(65*time.Minute))
}

func TestCheckDuration(t *testing.T) {
	start := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	now := start.Add(5 * time.Minute)

	finished := model.Check{StartedAt: start, CompletedAt: start.Add(30 * time.Second)}
	assert.Equal(t, 30*time.Second, finished.Duration(now))

	running := model.Check{StartedAt: start}
	assert.Equal(t, 5*time.Minute, running.Duration(now), "a running check is timed against now")

	assert.Zero(t, model.Check{}.Duration(now), "a check without timings has no duration")

	skewed := model.Check{StartedAt: now, CompletedAt: start}
	assert.Zero(t, skewed.Duration(now), "clock skew never yields a negative duration")
}

func TestKeyOwner(t *testing.T) {
	owner, name := model.Key{Repo: "relloyd/prutil"}.Owner()
	assert.Equal(t, "relloyd", owner)
	assert.Equal(t, "prutil", name)

	owner, name = model.Key{Repo: "prutil"}.Owner()
	assert.Empty(t, owner)
	assert.Equal(t, "prutil", name)

	require.Equal(t, "relloyd/prutil#42", model.Key{Repo: "relloyd/prutil", Number: 42}.String())
}

func TestSortByClosedDescOrdersMostRecentFirst(t *testing.T) {
	base := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	prs := []model.PullRequest{
		{Repo: "b/one", Number: 2, ClosedAt: base.Add(-48 * time.Hour)},
		{Repo: "a/two", Number: 9, ClosedAt: base},
		// Two closed in the same instant, to pin the tie-break.
		{Repo: "b/one", Number: 1, ClosedAt: base.Add(-24 * time.Hour)},
		{Repo: "a/two", Number: 3, ClosedAt: base.Add(-24 * time.Hour)},
	}

	model.SortByClosedDesc(prs)

	got := make([]string, 0, len(prs))
	for _, pr := range prs {
		got = append(got, pr.Key().String())
	}
	assert.Equal(t, []string{"a/two#9", "a/two#3", "b/one#1", "b/one#2"}, got,
		"ties break by repository then number, so the order is stable")
}

func TestTopNPerRepo(t *testing.T) {
	prs := func(keys ...string) []model.PullRequest {
		out := make([]model.PullRequest, 0, len(keys))
		for i, k := range keys {
			out = append(out, model.PullRequest{Repo: k, Number: i})
		}
		return out
	}

	tests := []struct {
		name string
		in   []model.PullRequest
		n    int
		want int
	}{
		{"keeps everything under the cap", prs("a/a", "b/b"), 3, 2},
		{"trims the busy repository", prs("a/a", "a/a", "a/a", "a/a", "b/b"), 3, 4},
		{"one each", prs("a/a", "a/a", "b/b", "b/b"), 1, 2},
		{"a cap of zero keeps nothing", prs("a/a"), 0, 0},
		{"a negative cap keeps nothing", prs("a/a"), -1, 0},
		{"empty input", nil, 3, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := model.TopNPerRepo(tc.in, tc.n)
			assert.Len(t, got, tc.want)

			perRepo := map[string]int{}
			for _, pr := range got {
				perRepo[pr.Repo]++
			}
			for repo, count := range perRepo {
				assert.LessOrEqual(t, count, tc.n, "%s is capped", repo)
			}
		})
	}
}

func TestTopNPerRepoPreservesOrderAndLeavesTheInputAlone(t *testing.T) {
	in := []model.PullRequest{
		{Repo: "a/a", Number: 1},
		{Repo: "b/b", Number: 2},
		{Repo: "a/a", Number: 3},
		{Repo: "a/a", Number: 4},
	}

	got := model.TopNPerRepo(in, 2)

	assert.Equal(t, []int{1, 2, 3}, []int{got[0].Number, got[1].Number, got[2].Number},
		"the caller's ordering survives, so sorting first is what decides which are kept")
	assert.Len(t, in, 4, "the input slice is not modified")
}

func TestParsePRState(t *testing.T) {
	tests := map[string]model.PRState{
		"MERGED":   model.PRStateMerged,
		"merged":   model.PRStateMerged,
		"CLOSED":   model.PRStateClosed,
		"OPEN":     model.PRStateOpen,
		"":         model.PRStateOpen,
		"NONSENSE": model.PRStateOpen,
	}

	for in, want := range tests {
		t.Run(in, func(t *testing.T) {
			assert.Equal(t, want, model.ParsePRState(in))
		})
	}

	assert.Equal(t, "MERGED", model.PRStateMerged.String())
	assert.Equal(t, "CLOSED", model.PRStateClosed.String())
	assert.Empty(t, model.PRStateOpen.String(), "an open pull request needs no badge")
}

// threads returns two open conversations and one the viewer answered
// themselves, which is the shape every dedup question turns on.
func threads() []model.ReviewThread {
	return []model.ReviewThread{
		{ID: "T1", Opener: "reviewer", LatestBy: "reviewer", LatestID: "C1"},
		{ID: "T2", Opener: "reviewer", LatestBy: "relloyd", LatestID: "C2"},
		{ID: "T3", Opener: "reviewer", LatestBy: "reviewer", LatestID: "C3", Resolved: true},
		{ID: "T4", Opener: "reviewer", LatestBy: "reviewer", LatestID: "C4", Outdated: true},
	}
}

func TestAThreadNeedsAttentionUntilItIsResolvedOrTheViewerHasTheLastWord(t *testing.T) {
	all := threads()
	rf := model.ReviewFilter{Viewer: "relloyd", Marker: model.DefaultSelfTestMarker}
	assert.True(t, all[0].NeedsAttention(rf))
	assert.False(t, all[1].NeedsAttention(rf), "the viewer already answered it")
	assert.False(t, all[2].NeedsAttention(rf), "resolved is finished with")
	assert.True(t, all[3].NeedsAttention(rf), "outdated lines may still hide an unanswered point")
}

func TestAThreadNeedsAttentionWhoeverTheViewerIsWhenThereIsNoViewer(t *testing.T) {
	assert.True(t, model.ReviewThread{LatestBy: "relloyd"}.NeedsAttention(model.ReviewFilter{Viewer: "", Marker: model.DefaultSelfTestMarker}),
		"without a login prutil cannot rule a thread out, so it does not")
}

func TestTheViewerLoginIsMatchedWithoutRegardToCase(t *testing.T) {
	assert.False(t, model.ReviewThread{LatestBy: "RelLoyd"}.NeedsAttention(model.ReviewFilter{Viewer: "relloyd", Marker: model.DefaultSelfTestMarker}))
}

func TestSelfAuthoredTestMarkerMakesAnUnresolvedThreadEligible(t *testing.T) {
	cases := []struct {
		name   string
		thread model.ReviewThread
		want   bool
	}{
		{
			name:   "a marked thread opened by the viewer remains eligible",
			thread: model.ReviewThread{Opener: "ReLloYd", LatestBy: "relloyd", Body: "Please test this.\n" + model.DefaultSelfTestMarker},
			want:   true,
		},
		{
			name: "a marked latest reply by the viewer remains eligible",
			thread: model.ReviewThread{
				Opener:     "reviewer",
				LatestBy:   "ReLloYd",
				LatestBody: "I am testing this.\n" + model.DefaultSelfTestMarker,
			},
			want: true,
		},
		{
			name:   "a self-authored thread without the exact marker remains excluded",
			thread: model.ReviewThread{Opener: "relloyd", LatestBy: "relloyd", Body: "<!-- PRUTIL:TEST -->"},
			want:   false,
		},
		{
			name: "a self-authored latest reply without the exact marker remains excluded",
			thread: model.ReviewThread{
				Opener: "reviewer", LatestBy: "relloyd", LatestBody: "<!-- PRUTIL:TEST -->",
			},
			want: false,
		},
		{
			name:   "a resolved marked thread remains excluded",
			thread: model.ReviewThread{Opener: "relloyd", LatestBy: "relloyd", Body: model.DefaultSelfTestMarker, Resolved: true},
			want:   false,
		},
		{
			name: "a resolved marked latest reply remains excluded",
			thread: model.ReviewThread{
				Opener: "reviewer", LatestBy: "relloyd", LatestBody: model.DefaultSelfTestMarker, Resolved: true,
			},
			want: false,
		},
		{
			name:   "another reviewer's marker does not override the viewer's reply",
			thread: model.ReviewThread{Opener: "reviewer", LatestBy: "relloyd", Body: model.DefaultSelfTestMarker},
			want:   false,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.thread.NeedsAttention(model.ReviewFilter{Viewer: "relloyd", Marker: model.DefaultSelfTestMarker}))
		})
	}
}

func TestFeedbackKeepsOnlyWhatIsStillWaitingAndInOrder(t *testing.T) {
	got := model.Feedback(threads(), model.ReviewFilter{Viewer: "relloyd", Marker: model.DefaultSelfTestMarker})
	require.Len(t, got, 2)
	assert.Equal(t, "T1", got[0].ID)
	assert.Equal(t, "T4", got[1].ID)
}

func TestUnhandledDropsThreadsAlreadyGivenToAnAgent(t *testing.T) {
	got := model.Unhandled(threads(), map[string]string{"T1": "C1"})

	ids := make([]string, 0, len(got))
	for _, thread := range got {
		ids = append(ids, thread.ID)
	}
	assert.Equal(t, []string{"T2", "T3", "T4"}, ids)
}

func TestAReplySinceTheHandoffMakesAThreadNewAgain(t *testing.T) {
	got := model.Unhandled(threads(), map[string]string{"T1": "C0"})
	require.NotEmpty(t, got)
	assert.Equal(t, "T1", got[0].ID, "the newest comment changed, so somebody has said something since")
}

func TestDigestPairsEveryThreadWithItsNewestComment(t *testing.T) {
	assert.Equal(t, map[string]string{"T1": "C1", "T2": "C2", "T3": "C3", "T4": "C4"},
		model.Digest(threads()))
}

func TestAnEmptyMarkerTurnsTheSelfTestExceptionOff(t *testing.T) {
	// The marker is configurable, and configuring it away must leave the
	// ordinary rule: a thread whose last word is yours is one you answered.
	thread := model.ReviewThread{
		Opener:     "me",
		Body:       "a note " + model.DefaultSelfTestMarker,
		LatestBy:   "me",
		LatestBody: "a note " + model.DefaultSelfTestMarker,
	}

	assert.True(t, thread.NeedsAttention(model.ReviewFilter{Viewer: "me", Marker: model.DefaultSelfTestMarker}), "marked and mine")
	assert.False(t, thread.NeedsAttention(model.ReviewFilter{Viewer: "me", Marker: ""}), "no marker configured, so no exception")
	assert.False(t, thread.NeedsAttention(model.ReviewFilter{Viewer: "me", Marker: "<!-- other -->"}), "a different marker is not this one")
}

func TestAnotherPersonsMarkerChangesNothing(t *testing.T) {
	// Nobody else can reach into what a reader's watcher counts as feedback.
	thread := model.ReviewThread{
		Opener:     "someone",
		Body:       "a note " + model.DefaultSelfTestMarker,
		LatestBy:   "me",
		LatestBody: "answered",
	}

	assert.False(t, thread.NeedsAttention(model.ReviewFilter{Viewer: "me", Marker: model.DefaultSelfTestMarker}),
		"the marker was not in a comment the viewer wrote")
}

func TestIsApproved(t *testing.T) {
	cases := []struct {
		name      string
		decision  model.ReviewDecision
		approvals int
		want      bool
	}{
		{"an approved decision is approved", model.ReviewApproved, 1, true},
		{"the decision wins over an approval the rules do not count yet", model.ReviewRequired, 1, false},
		{"a request for changes outweighs the approvals beside it", model.ReviewChangesRequested, 2, false},
		{"without rules an approval is the only sign there is", model.ReviewNone, 1, true},
		{"without rules and without an approval nothing is approved", model.ReviewNone, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, model.IsApproved(tc.decision, tc.approvals))
			assert.Equal(t, tc.want, model.PullRequest{ReviewDecision: tc.decision, Approvals: tc.approvals}.Approved())
			assert.Equal(t, tc.want, model.Snapshot{ReviewDecision: tc.decision, Approvals: tc.approvals}.Approved())
		})
	}
}

func TestASnapshotMovesWhenTheReviewersDo(t *testing.T) {
	base := model.Snapshot{HeadOID: "abc", ReviewDecision: model.ReviewRequired}

	decided := base
	decided.ReviewDecision = model.ReviewApproved
	assert.True(t, decided.Moved(base), "a new review decision is a change")

	approved := base
	approved.Approvals = 1
	assert.True(t, approved.Moved(base), "a new approval is a change")

	assert.False(t, base.Moved(base))
}

func TestIsAgentCommentIdentifiesAutomatedReplies(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "the tracking tag at the end of a reply",
			body: "Fixed the bug.\n<!-- prutil:agent -->",
			want: true,
		},
		{
			name: "the tracking tag on its own line above the reply",
			body: "<!-- prutil:agent -->\nAutomated reply",
			want: true,
		},
		{
			name: "an automated response header is prose, not provenance",
			body: "> automated response from prutil/herdr\n\nI have addressed the comments.",
			want: false,
		},
		{
			name: "an automated AI response header is prose, not provenance",
			body: "> automated AI response\n\nFixed in commit 12345.",
			want: false,
		},
		{
			name: "a quoted automated response a reviewer typed is not an agent comment",
			body: "> automated response\n\nHere are the details.",
			want: false,
		},
		{
			name: "ordinary human review comment",
			body: "Please update this function to handle nil pointers.",
			want: false,
		},
		{
			name: "self-test comment without agent tag",
			body: "Please test this.\n<!-- prutil:test -->",
			want: false,
		},
		{
			name: "a longer word starting with the tag is a different tag",
			body: "<!-- prutil:agentic-review -->\nNot ours.",
			want: false,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, model.IsAgentComment(tt.body))
		})
	}
}

func TestAgentReplyStopsMarkedThreadFromTriggeringSecondHandoff(t *testing.T) {
	rf := model.ReviewFilter{Viewer: "relloyd", Marker: model.DefaultSelfTestMarker}

	// 1. Thread opened by viewer with self-test marker.
	thread := model.ReviewThread{
		Opener:     "relloyd",
		Body:       "Please test this.\n" + model.DefaultSelfTestMarker,
		LatestBy:   "relloyd",
		LatestBody: "Please test this.\n" + model.DefaultSelfTestMarker,
		Comments:   1,
	}
	assert.True(t, thread.NeedsAttention(rf),
		"opening self-test comment needs attention")

	// 2. The viewer's agent replies, carrying the tracking tag.
	thread.LatestBody = "Fixed in a1b2c3d.\n" + model.AgentCommentMarker
	thread.Comments = 2
	assert.False(t, thread.NeedsAttention(rf),
		"agent reply must not re-trigger attention (loop prevented)")

	// 3. User replies with a follow-up test comment.
	thread.LatestBody = "Please also update the test.\n" + model.DefaultSelfTestMarker
	thread.Comments = 3
	assert.True(t, thread.NeedsAttention(rf),
		"human follow-up comment needs attention again")
}

func TestAnotherPersonsAgentMarkerDoesNotSilenceAThread(t *testing.T) {
	// The marker says "an agent of mine wrote this". A reviewer writing it,
	// deliberately or by quoting one, must not take their own feedback off a
	// reader's watcher.
	thread := model.ReviewThread{
		Opener:     "reviewer",
		Body:       "This leaks a file handle.",
		LatestBy:   "reviewer",
		LatestBody: "This leaks a file handle.\n" + model.AgentCommentMarker,
	}

	assert.True(t, thread.NeedsAttention(model.ReviewFilter{Viewer: "relloyd"}),
		"the marker was not in a comment the viewer wrote")
}

func TestSelfReviewModeTreatsAllViewerCommentsAsFeedbackUnlessAgentReplied(t *testing.T) {
	rfSelfReview := model.ReviewFilter{Viewer: "relloyd", SelfReview: true}
	rfStandard := model.ReviewFilter{Viewer: "relloyd", SelfReview: false}

	// 1. Thread opened by viewer with regular comment (NO marker).
	thread := model.ReviewThread{
		Opener:     "relloyd",
		Body:       "Please refactor this method to avoid allocations.",
		LatestBy:   "relloyd",
		LatestBody: "Please refactor this method to avoid allocations.",
		Comments:   1,
	}
	assert.False(t, thread.NeedsAttention(rfStandard), "standard mode ignores viewer's unmarked comment")
	assert.True(t, thread.NeedsAttention(rfSelfReview), "self-review mode treats viewer comment as actionable")

	// 2. Agent replies with tracking tag.
	thread.LatestBody = "Refactored in 4f8b21a.\n" + model.AgentCommentMarker
	thread.Comments = 2
	assert.False(t, thread.NeedsAttention(rfSelfReview), "agent reply is ignored even in self-review mode")

	// 3. Human leaves a new unmarked follow-up reply.
	thread.LatestBody = "Thanks, also benchmark this against the old version."
	thread.Comments = 3
	assert.True(t, thread.NeedsAttention(rfSelfReview), "new human reply in self-review mode needs attention again")

	// 4. Thread is resolved on GitHub.
	thread.Resolved = true
	assert.False(t, thread.NeedsAttention(rfSelfReview), "resolved thread is ignored even in self-review mode")
}

func TestSelfReviewModeWaitsForTheViewerToSubmitAComment(t *testing.T) {
	thread := model.ReviewThread{
		LatestBy:      "relloyd",
		LatestBody:    "Please fix this before submitting the review.",
		LatestPending: true,
	}

	assert.False(t, thread.NeedsAttention(model.ReviewFilter{
		Viewer:     "relloyd",
		SelfReview: true,
	}), "a pending review draft is not feedback until it is submitted")
}

// trustPolicy is the shipped default, which every trust case starts from.
func trustPolicy() model.TrustPolicy {
	return model.TrustPolicy{
		Viewer:       "relloyd",
		Associations: []string{"OWNER", "COLLABORATOR"},
		Authors:      []string{"gemini-code-assist[bot]"},
	}
}

// thread builds an unresolved thread whose participants are the whole of it.
func thread(id string, participants ...model.Participant) model.ReviewThread {
	return model.ReviewThread{
		ID:                   id,
		Participants:         participants,
		ParticipantsComplete: true,
	}
}

func TestTrustDecidesOneParticipantAtATime(t *testing.T) {
	cases := []struct {
		name    string
		who     model.Participant
		trusted bool
	}{
		{
			name:    "the viewer is trusted whatever GitHub calls their association",
			who:     model.Participant{Login: "relloyd", Association: "NONE"},
			trusted: true,
		},
		{
			name:    "a login differing only in case is still the viewer",
			who:     model.Participant{Login: "RelLoyd", Association: "NONE"},
			trusted: true,
		},
		{
			name:    "a collaborator is trusted by association alone",
			who:     model.Participant{Login: "reviewer", Association: "COLLABORATOR"},
			trusted: true,
		},
		{
			name:    "an organisation member is not, because membership implies no write access",
			who:     model.Participant{Login: "colleague", Association: "MEMBER"},
			trusted: false,
		},
		{
			name:    "a stranger is not",
			who:     model.Participant{Login: "mallory", Association: "NONE"},
			trusted: false,
		},
		{
			name:    "a configured bot is trusted when GitHub says it is an app",
			who:     model.Participant{Login: "gemini-code-assist", Association: "NONE", Bot: true},
			trusted: true,
		},
		{
			name:    "a person who registered the bot's login is not",
			who:     model.Participant{Login: "gemini-code-assist", Association: "NONE"},
			trusted: false,
		},
		{
			name:    "an author GitHub has lost is nobody",
			who:     model.Participant{Association: "OWNER"},
			trusted: false,
		},
		{
			name:    "an empty association matches no configured association",
			who:     model.Participant{Login: "mallory"},
			trusted: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hold := model.HoldFor([]model.ReviewThread{thread("PRRT_1", tc.who)}, trustPolicy())
			assert.Equal(t, !tc.trusted, hold.Held(), tc.name)
		})
	}
}

func TestATrustedAuthorEntryWithoutTheBotSuffixMatchesOnlyAPerson(t *testing.T) {
	policy := trustPolicy()
	policy.Authors = []string{"dependabot"}

	person := model.Participant{Login: "dependabot", Association: "NONE"}
	app := model.Participant{Login: "dependabot", Association: "NONE", Bot: true}

	assert.False(t, model.HoldFor([]model.ReviewThread{thread("PRRT_1", person)}, policy).Held(),
		"the entry names a person, and this is that person")
	assert.True(t, model.HoldFor([]model.ReviewThread{thread("PRRT_1", app)}, policy).Held(),
		"the same name as an app is a different account, and the entry does not name it")
}

func TestAnUntrustedReplyBetweenTrustedOnesStillHolds(t *testing.T) {
	hold := model.HoldFor([]model.ReviewThread{thread("PRRT_1",
		model.Participant{Login: "reviewer", Association: "COLLABORATOR"},
		model.Participant{Login: "mallory", Association: "NONE"},
		model.Participant{Login: "relloyd", Association: "OWNER"},
	)}, trustPolicy())

	require.True(t, hold.Held(), "the first and last comment cannot answer for the middle of a thread")
	assert.Equal(t, []string{"mallory"}, hold.Authors)
}

func TestAnIncompleteParticipantListHoldsWithoutNamingAnybody(t *testing.T) {
	partial := thread("PRRT_1", model.Participant{Login: "reviewer", Association: "COLLABORATOR"})
	partial.ParticipantsComplete = false

	hold := model.HoldFor([]model.ReviewThread{partial}, trustPolicy())

	require.True(t, hold.Held(), "not knowing who spoke is not the same as knowing")
	assert.True(t, hold.Unknown)
	assert.Empty(t, hold.Authors, "there is nobody to name; that is the point")
}

func TestResolvingAThreadReleasesTheHoldItCaused(t *testing.T) {
	hostile := thread("PRRT_1", model.Participant{Login: "mallory", Association: "NONE"})
	require.True(t, model.HoldFor([]model.ReviewThread{hostile}, trustPolicy()).Held())

	hostile.Resolved = true
	assert.False(t, model.HoldFor([]model.ReviewThread{hostile}, trustPolicy()).Held(),
		"resolving it on GitHub is how a reader clears a hold for good")
}

func TestAHoldNamesEachUntrustedAuthorOnceInTheOrderMet(t *testing.T) {
	hold := model.HoldFor([]model.ReviewThread{
		thread("PRRT_1",
			model.Participant{Login: "mallory", Association: "NONE"},
			model.Participant{Login: "trudy", Association: "FIRST_TIME_CONTRIBUTOR"},
		),
		thread("PRRT_2",
			model.Participant{Login: "Mallory", Association: "NONE"},
			model.Participant{Association: "NONE"},
		),
	}, trustPolicy())

	assert.Equal(t, []string{"mallory", "trudy", model.DeletedAccount}, hold.Authors,
		"the same person under two spellings is one name in the notice")
}

func TestAnEmptyViewerTrustsNobodyByThatRoute(t *testing.T) {
	policy := model.TrustPolicy{Associations: []string{"OWNER"}}

	hold := model.HoldFor([]model.ReviewThread{thread("PRRT_1",
		model.Participant{Login: "somebody", Association: "NONE"},
	)}, policy)

	assert.True(t, hold.Held(), "an unknown viewer must not make every login match")
}

func TestATrustedThreadHoldsNothing(t *testing.T) {
	hold := model.HoldFor([]model.ReviewThread{
		thread("PRRT_1", model.Participant{Login: "relloyd", Association: "OWNER"}),
		thread("PRRT_2", model.Participant{Login: "gemini-code-assist", Association: "NONE", Bot: true}),
	}, trustPolicy())

	assert.False(t, hold.Held())
	assert.Empty(t, hold.Authors)
}

// commented builds an unresolved thread with a body and a reply, whose
// participants are trusted, so that only the hidden-content rule can hold it.
func commented(opener, body, latestBy, latestBody string, who ...model.Participant) model.ReviewThread {
	if len(who) == 0 {
		who = []model.Participant{
			{Login: opener, Association: "COLLABORATOR"},
			{Login: latestBy, Association: "COLLABORATOR"},
		}
	}
	return model.ReviewThread{
		ID: "PRRT_1", Opener: opener, Body: body,
		LatestBy: latestBy, LatestBody: latestBody,
		Participants: who, ParticipantsComplete: true,
	}
}

func TestHiddenCharactersHoldAPullRequestWhoeverWroteThem(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		hidden bool
	}{
		{
			name:   "ordinary prose is not hidden text",
			body:   "This retries forever. Please add a ceiling.",
			hidden: false,
		},
		{
			name:   "a tag character carries ASCII invisibly",
			body:   "Looks fine to me\U000E0041\U000E0042",
			hidden: true,
		},
		{
			name:   "a zero-width space between words hides a boundary",
			body:   "Looks\u200bfine to me",
			hidden: true,
		},
		{
			name:   "a word joiner counts too",
			body:   "Looks\u2060fine",
			hidden: true,
		},
		{
			name:   "a byte order mark in the middle of prose counts",
			body:   "Looks fine\ufeff to me",
			hidden: true,
		},
		{
			name:   "a right-to-left override can reorder what is displayed",
			body:   "rm -rf \u202etxt.elif",
			hidden: true,
		},
		{
			name:   "an isolate counts as a bidi control",
			body:   "see \u2066this\u2069",
			hidden: true,
		},
		{
			name:   "a family emoji is joiners between pictographs, and is ordinary",
			body:   "ship it \U0001F468\u200d\U0001F469\u200d\U0001F467",
			hidden: false,
		},
		{
			name:   "so is a profession emoji, which joins on a symbol",
			body:   "nice work \U0001F468\u200d\u2695\ufe0f",
			hidden: false,
		},
		{
			name:   "and a flag, which joins on the variation selector",
			body:   "\U0001F3F3\ufe0f\u200d\U0001F308 merged",
			hidden: false,
		},
		{
			name:   "a joiner between letters is not an emoji",
			body:   "loo\u200dks fine",
			hidden: true,
		},
		{
			name:   "two joiners in a row are not an emoji either",
			body:   "\U0001F468\u200d\u200d\U0001F469",
			hidden: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hold := model.HoldFor(
				[]model.ReviewThread{commented("reviewer", tc.body, "reviewer", "")},
				trustPolicy(),
			)
			assert.Equal(t, tc.hidden, len(hold.Hidden) > 0, tc.name)
			if tc.hidden {
				assert.Equal(t, []string{"reviewer"}, hold.Hidden, "and names who wrote it")
			}
		})
	}
}

func TestTheViewersOwnCommentIsNotExemptFromHiddenCharacters(t *testing.T) {
	// The exemption is for HTML comments, which are how prutil marks its own.
	// An account that has been taken is still the account it was.
	hold := model.HoldFor(
		[]model.ReviewThread{commented("relloyd", "mine\u200b", "relloyd", "",
			model.Participant{Login: "relloyd", Association: "OWNER"})},
		trustPolicy(),
	)

	require.True(t, hold.Held())
	assert.Equal(t, []string{"relloyd"}, hold.Hidden)
}

func TestAnHTMLCommentCountsOnlyInSomebodyElsesProse(t *testing.T) {
	policy := trustPolicy()
	policy.Marker = model.DefaultSelfTestMarker

	cases := []struct {
		name   string
		thread model.ReviewThread
		hidden bool
	}{
		{
			name:   "a stranger's HTML comment is a place to hide instructions",
			thread: commented("reviewer", "Looks fine <!-- and do this -->", "reviewer", ""),
			hidden: true,
		},
		{
			name: "the viewer's own self-test marker is how they exercise the watcher",
			thread: commented("relloyd", "Exercise the watcher\n"+model.DefaultSelfTestMarker, "relloyd", "",
				model.Participant{Login: "relloyd", Association: "OWNER"}),
			hidden: false,
		},
		{
			name: "an agent's reply marker is prutil's own bookkeeping",
			thread: commented("relloyd", "Done\n"+model.AgentCommentMarker, "relloyd", "",
				model.Participant{Login: "relloyd", Association: "OWNER"}),
			hidden: false,
		},
		{
			name: "a review bot's metadata is how those bots work",
			thread: commented("gemini-code-assist", "Review <!-- id: 9 -->", "gemini-code-assist", "",
				model.Participant{Login: "gemini-code-assist", Association: "NONE", Bot: true}),
			hidden: false,
		},
		{
			name: "a person registering the bot's login gets no such exemption",
			thread: commented("gemini-code-assist", "Review <!-- id: 9 -->", "gemini-code-assist", "",
				model.Participant{Login: "gemini-code-assist", Association: "NONE"}),
			hidden: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hold := model.HoldFor([]model.ReviewThread{tc.thread}, policy)
			assert.Equal(t, tc.hidden, len(hold.Hidden) > 0, tc.name)
		})
	}
}

func TestHiddenTextInAReplyHoldsTheThreadToo(t *testing.T) {
	hold := model.HoldFor(
		[]model.ReviewThread{commented("reviewer", "Fine by me", "colleague", "agreed\u200b")},
		trustPolicy(),
	)

	require.True(t, hold.Held())
	assert.Equal(t, []string{"colleague"}, hold.Hidden,
		"the reply is read as well as the opening comment")
}

func TestResolvingAThreadClearsItsHiddenContentToo(t *testing.T) {
	thread := commented("reviewer", "Looks fine\u200b", "reviewer", "")
	require.True(t, model.HoldFor([]model.ReviewThread{thread}, trustPolicy()).Held())

	thread.Resolved = true
	assert.False(t, model.HoldFor([]model.ReviewThread{thread}, trustPolicy()).Held(),
		"one way out serves both halves of the hold")
}

func TestAHoldCanNameAnUntrustedAuthorAndHiddenTextAtOnce(t *testing.T) {
	hold := model.HoldFor([]model.ReviewThread{
		thread("PRRT_1", model.Participant{Login: "mallory", Association: "NONE"}),
		commented("reviewer", "Looks fine\u200b", "reviewer", ""),
	}, trustPolicy())

	assert.Equal(t, []string{"mallory"}, hold.Authors)
	assert.Equal(t, []string{"reviewer"}, hold.Hidden,
		"a trusted author's account is exactly the one worth compromising")
}
