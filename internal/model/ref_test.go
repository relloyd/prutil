package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/relloyd/prutil/internal/model"
)

func TestParseReferenceReadsAPullRequestTheWayItIsPasted(t *testing.T) {
	want := model.Key{Repo: "acme/widgets", Number: 12}
	cases := []struct {
		name string
		text string
	}{
		{name: "a github.com URL", text: "https://github.com/acme/widgets/pull/12"},
		{name: "a URL copied from the files tab", text: "https://github.com/acme/widgets/pull/12/files"},
		{name: "a URL copied from a comment", text: "https://github.com/acme/widgets/pull/12#discussion_r99"},
		{name: "a URL with a trailing slash", text: "https://github.com/acme/widgets/pull/12/"},
		{name: "an enterprise URL", text: "https://git.example.com/acme/widgets/pull/12"},
		{name: "GitHub's own shorthand", text: "acme/widgets#12"},
		{name: "the shorthand with surrounding space", text: "  acme/widgets#12\n"},
		{name: "a URL without its host", text: "acme/widgets/pull/12"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := model.ParseReference(tc.text)
			require.NoError(t, err)
			assert.Equal(t, want, got)
		})
	}
}

func TestParseReferenceRefusesWhatIsNotAPullRequest(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{name: "nothing at all", text: ""},
		{name: "a repository without a number", text: "acme/widgets"},
		{name: "an issue URL", text: "https://github.com/acme/widgets/issues/12"},
		{name: "a number that is not one", text: "acme/widgets#twelve"},
		{name: "number zero", text: "acme/widgets#0"},
		{name: "a repository with no owner", text: "widgets#12"},
		{name: "a repository name with a space in it", text: "acme/wid gets#12"},
		{name: "a repository URL", text: "https://github.com/acme/widgets"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := model.ParseReference(tc.text)
			assert.ErrorIs(t, err, model.ErrNotAReference)
		})
	}
}

// agentReplyThread is an unresolved thread whose newest comment is an agent's
// reply posted by login.
func agentReplyThread(login, commentID string) model.ReviewThread {
	return model.ReviewThread{
		ID: "T-" + commentID, Opener: "reviewer", Body: "Please fix.",
		LatestBy: login, LatestID: commentID, LatestBody: "Done.\n\n" + model.AgentCommentMarker,
		Participants: []model.Participant{
			{Login: "reviewer", Association: "COLLABORATOR"},
			{Login: login, Association: "COLLABORATOR"},
		},
		ParticipantsComplete: true,
	}
}

func TestOtherAgentRepliesFindsAnAgentReplyPostedAsSomebodyElse(t *testing.T) {
	threads := []model.ReviewThread{agentReplyThread("alice", "C1")}

	got := model.OtherAgentReplies(threads, "relloyd")

	assert.Equal(t, []model.AgentReply{{Login: "alice", CommentID: "C1"}}, got)
}

func TestOtherAgentRepliesIgnoresWhatIsNotAnotherAgentStillAnswering(t *testing.T) {
	resolved := agentReplyThread("alice", "C1")
	resolved.Resolved = true
	pending := agentReplyThread("alice", "C2")
	pending.LatestPending = true
	superseded := agentReplyThread("alice", "C3")
	superseded.LatestBy, superseded.LatestBody = "reviewer", "Not quite."

	cases := []struct {
		name   string
		thread model.ReviewThread
	}{
		{name: "the viewer's own agent's reply", thread: agentReplyThread("RELLOYD", "C0")},
		{name: "a resolved thread", thread: resolved},
		{name: "a reply GitHub has not published", thread: pending},
		{name: "a thread somebody has replied to since", thread: superseded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Empty(t, model.OtherAgentReplies([]model.ReviewThread{tc.thread}, "relloyd"))
		})
	}
}

func TestOtherAgentRepliesCountsEveryMarkerWhenTheViewerIsUnknown(t *testing.T) {
	// Not knowing whose reply it was is not knowing it was ours.
	got := model.OtherAgentReplies([]model.ReviewThread{agentReplyThread("relloyd", "C1")}, "")

	assert.Len(t, got, 1)
}

func TestAnotherAgentsReplyHoldsEvenWhenEveryoneIsTrusted(t *testing.T) {
	threads := []model.ReviewThread{agentReplyThread("alice", "C1"), agentReplyThread("Alice", "C2")}

	hold := model.HoldFor(threads, model.TrustPolicy{Viewer: "relloyd", Associations: []string{"COLLABORATOR"}})

	assert.True(t, hold.Held(), "trusting alice is not the question: her agent is answering, and ours would answer it")
	assert.Empty(t, hold.Authors)
	assert.Equal(t, []string{"alice"}, hold.OtherAgents, "each is named once")
}

func TestAReviewerTypingTheMarkerCanOnlyHoldThePullRequest(t *testing.T) {
	// The marker is text anybody can type. In a stranger's hands it must not be
	// a way to take feedback off the list, so the thread is still feedback.
	thread := agentReplyThread("mallory", "C1")

	assert.True(t, thread.NeedsAttention(model.ReviewFilter{Viewer: "relloyd"}))
	assert.True(t, model.HoldFor([]model.ReviewThread{thread}, model.TrustPolicy{Viewer: "relloyd"}).Held())
}

func TestValidRepoAcceptsOnlyAnOwnerAndAName(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{name: "an owner and a name", text: "acme/widgets", want: true},
		{name: "dots, dashes and underscores", text: "acme-co/wid.get_s", want: true},
		{name: "no owner", text: "widgets", want: false},
		{name: "a space, which would add a search qualifier", text: "acme/widgets is:closed", want: false},
		{name: "a third part", text: "acme/widgets/pull", want: false},
		{name: "an empty name", text: "acme/", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, model.ValidRepo(tc.text))
		})
	}
}
