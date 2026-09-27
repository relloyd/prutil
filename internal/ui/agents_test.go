package ui

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/relloyd/prutil/internal/gh"
	"github.com/relloyd/prutil/internal/handoff"
	"github.com/relloyd/prutil/internal/home"
	"github.com/relloyd/prutil/internal/model"
)

// answeredByAnotherAgent is the sample feedback with one thread's newest
// comment an agent's reply posted as somebody other than the viewer: what a
// second prutil watching the same pull request leaves behind.
func answeredByAnotherAgent(commentID string) gh.Review {
	review := sampleThreads()
	review.Threads[0].LatestBy = "alice"
	review.Threads[0].LatestID = commentID
	review.Threads[0].LatestBody = "Fixed in abc123.\n\n" + model.AgentCommentMarker
	review.Threads[0].Participants = append(review.Threads[0].Participants,
		model.Participant{Login: "alice", Association: "COLLABORATOR"})
	return review
}

// read delivers a review read as the watcher's own, rather than as the manual
// discovery N asks for.
func read(t *testing.T, app *App, key model.Key, review gh.Review) {
	t.Helper()
	pump(t, app, send(t, app, watchReviewMsg{key: key, review: review}))
}

var ownKey = model.Key{Repo: "relloyd/prutil", Number: 42}

func TestAnotherAgentsReplyStopsTheWatch(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	dispatcher := dispatcherOf(t, app)
	send(t, app, press("w"))
	require.True(t, app.armed(ownKey))

	read(t, app, ownKey, answeredByAnotherAgent("CX1"))

	assert.False(t, app.armed(ownKey), "two agents taking turns on one branch is the reader's call")
	assert.Empty(t, dispatcher.requests(), "and nothing is handed over")
	history, err := app.store.RecentHandoffs(ownKey.String(), 10)
	require.NoError(t, err)
	require.NotEmpty(t, history)
	assert.Equal(t, home.OutcomeHeld, history[0].Outcome)
	assert.Equal(t, "stopped watching: another agent is replying as alice", history[0].Detail)
	toasts := dispatcher.notifications()
	require.Len(t, toasts, 1, "the reader is told, since nothing else will come back to tell them")
	assert.Equal(t, "relloyd/prutil#42: stopped watching", toasts[0].title)

	stored, err := app.store.LoadState()
	require.NoError(t, err)
	assert.False(t, stored.Armed(ownKey.String()), "and it stays stopped across a restart")
}

func TestAnotherAgentsReplyHoldsThePullRequest(t *testing.T) {
	hold := model.HoldFor(answeredByAnotherAgent("CX1").Threads, model.TrustPolicy{
		Viewer: "relloyd", Associations: []string{"COLLABORATOR"},
	})

	assert.True(t, hold.Held())
	assert.Equal(t, []string{"alice"}, hold.OtherAgents)
	assert.Equal(t, "another agent replying as alice", holdReason(hold))
}

func TestTheReadersOwnAgentReplyDoesNotStopTheWatch(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	send(t, app, press("w"))
	review := answeredByAnotherAgent("CX1")
	review.Threads[0].LatestBy = "relloyd"

	read(t, app, ownKey, review)

	assert.True(t, app.armed(ownKey))
	assert.Empty(t, app.runtimeOf(ownKey).hold.OtherAgents)
}

func TestWatchingAgainAfterAnotherAgentRepliedAsksFirst(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	send(t, app, press("w"))
	read(t, app, ownKey, answeredByAnotherAgent("CX1"))
	require.False(t, app.armed(ownKey))

	asked := send(t, app, press("w"))
	require.NotNil(t, asked)
	question := string(asked().(statusMsg))
	assert.Contains(t, question, "another agent is replying as alice")
	assert.Contains(t, question, "press w again")
	require.False(t, app.armed(ownKey))

	send(t, app, press("w"))
	assert.True(t, app.armed(ownKey), "the second press is the reader deciding")
}

func TestAReplyTheReaderHasAcceptedDoesNotStopTheWatchAgain(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	dispatcher := dispatcherOf(t, app)
	send(t, app, press("w"))
	read(t, app, ownKey, answeredByAnotherAgent("CX1"))
	send(t, app, press("w"))
	send(t, app, press("w"))
	require.True(t, app.armed(ownKey))

	read(t, app, ownKey, answeredByAnotherAgent("CX1"))

	assert.True(t, app.armed(ownKey), "the reader has seen this reply and chose to watch anyway")
	assert.Empty(t, dispatcher.requests(),
		"the pull request is still held: the other agent answered, so there is nothing for this one to do")
}

func TestANewReplyFromAnotherAgentStopsTheWatchAgain(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	send(t, app, press("w"))
	read(t, app, ownKey, answeredByAnotherAgent("CX1"))
	send(t, app, press("w"))
	send(t, app, press("w"))
	require.True(t, app.armed(ownKey))

	read(t, app, ownKey, answeredByAnotherAgent("CX2"))

	assert.False(t, app.armed(ownKey))
}

func TestManualDiscoveryDoesNotStopTheWatch(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	client.review = answeredByAnotherAgent("CX1")
	send(t, app, press("w"))

	notifyNewFeedback(t, app)

	assert.True(t, app.armed(ownKey), "N is the reader asking, not the watcher finding")
	assert.True(t, app.runtimeOf(ownKey).hold.Held(), "but the hold still stands")
}

func TestWSendsFeedbackPastAnotherAgentAfterASecondPress(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	client.review = answeredByAnotherAgent("CX1")
	dispatcher := dispatcherOf(t, app)
	dispatcher.result = handoff.Result{Outcome: home.OutcomeSent, Target: "w1:p1", Kind: "claude"}
	notifyNewFeedback(t, app)
	require.True(t, app.runtimeOf(ownKey).hold.Held())

	asked := send(t, app, press("W"))
	require.NotNil(t, asked)
	assert.Contains(t, string(asked().(statusMsg)), "another agent replying as alice")
	require.Empty(t, dispatcher.requests())

	handOver(t, app)
	assert.Len(t, dispatcher.requests(), 1)
}
