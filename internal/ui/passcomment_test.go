package ui

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/relloyd/prutil/internal/model"
)

// passKey is the pull request the pass-comment tests arm: the one the list
// cursor starts on.
var passKey = model.Key{Repo: "relloyd/prutil", Number: 42}

// passApp builds an app with a checks passed comment configured, and the
// selected pull request watched with its checks still running.
func passApp(t *testing.T) (*App, *fakeClient) {
	t.Helper()
	app, client, _ := newTestApp(t, 120, 40)
	app.homeCfg.ChecksPassed.Comment = "/deploy staging"
	setRollup(client, "abc", model.StatusPending)

	send(t, app, press("w"))
	nextPoll(t, app)
	return app, client
}

// setRollup is what GitHub will say about the selected pull request's head at
// the next poll.
func setRollup(client *fakeClient, head string, rollup model.Status) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.snapshots["PR_42"] = model.Snapshot{NodeID: "PR_42", HeadOID: head, UpdatedAt: testNow, Rollup: rollup}
}

// nextPoll moves the clock past the watcher's interval and drives one beat.
func nextPoll(t *testing.T, app *App) {
	t.Helper()
	advance(app, time.Second)
	poll(t, app)
}

// passComments are the comments posted with the checks passed text.
func passComments(client *fakeClient) []commentRecord {
	var out []commentRecord
	for _, c := range client.comments() {
		if c.body == "/deploy staging" {
			out = append(out, c)
		}
	}
	return out
}

func TestTheChecksPassedCommentIsPostedOncePerHeadCommit(t *testing.T) {
	app, client := passApp(t)
	send(t, app, press("P"))
	require.True(t, app.state.PostOnPass(passKey.String()))

	nextPoll(t, app)
	assert.Empty(t, passComments(client), "nothing is posted while the checks are still running")

	setRollup(client, "abc", model.StatusSuccess)
	nextPoll(t, app)
	require.Len(t, passComments(client), 1, "the checks passing posts the comment")
	assert.Equal(t, "PR_42", passComments(client)[0].subjectID)
	assert.Equal(t, "abc", app.state.Get(passKey.String()).LastPassCommentHead)

	nextPoll(t, app)
	assert.Len(t, passComments(client), 1, "a poll finding the same commit still green posts nothing")

	setRollup(client, "def", model.StatusSuccess)
	nextPoll(t, app)
	assert.Len(t, passComments(client), 2, "a new commit whose checks pass is posted for again")
}

func TestPressingPAgainStopsTheChecksPassedComment(t *testing.T) {
	app, client := passApp(t)
	send(t, app, press("P"))
	send(t, app, press("P"))
	require.False(t, app.state.PostOnPass(passKey.String()))

	setRollup(client, "abc", model.StatusSuccess)
	nextPoll(t, app)
	assert.Empty(t, passComments(client))
}

func TestPRefusesWhatItCannotAct(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(app *App)
		want    string
	}{
		{
			name:    "a pull request that is not watched is told to be watched first",
			prepare: func(app *App) { app.homeCfg.ChecksPassed.Comment = "/deploy staging" },
			want:    "relloyd/prutil#42 is not watched · press w to watch it, then P",
		},
		{
			name: "a repository with no comment configured says where to set one",
			prepare: func(app *App) {
				send(t, app, press("w"))
			},
			want: "no checks passed comment for relloyd/prutil · set one in settings (s)",
		},
		{
			name: "a repository whose override is empty is switched off there",
			prepare: func(app *App) {
				app.homeCfg.ChecksPassed.Comment = "/deploy staging"
				app.homeCfg.ChecksPassed.Repos = map[string]string{"relloyd/prutil": ""}
				send(t, app, press("w"))
			},
			want: "no checks passed comment for relloyd/prutil · set one in settings (s)",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			app, _, _ := newTestApp(t, 120, 40)
			tc.prepare(app)
			cmd := send(t, app, press("P"))
			require.NotNil(t, cmd)
			assert.Equal(t, statusMsg(tc.want), cmd())
			assert.False(t, app.state.PostOnPass(passKey.String()))
		})
	}
}

func TestArmingOnChecksThatHaveAlreadyPassedAsksFirst(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	app.homeCfg.ChecksPassed.Comment = "/deploy staging"
	send(t, app, press("w"))
	nextPoll(t, app)

	cmd := send(t, app, press("P"))
	require.NotNil(t, cmd)
	assert.Equal(t, statusMsg("checks have already passed on relloyd/prutil#42 · press P again to post /deploy staging now"), cmd())
	assert.False(t, app.state.PostOnPass(passKey.String()), "the first press arms nothing")

	send(t, app, press("P"))
	require.True(t, app.state.PostOnPass(passKey.String()))
	nextPoll(t, app)
	assert.Len(t, passComments(client), 1, "the second press posts at the next poll")
}

func TestAHeldPullRequestGetsNoChecksPassedComment(t *testing.T) {
	app, client := passApp(t)
	client.mu.Lock()
	client.review = hostileReview()
	client.mu.Unlock()
	send(t, app, press("P"))
	// The threads are read again, and found held.
	app.mutate(passKey).holdKnown = false

	setRollup(client, "abc", model.StatusSuccess)
	nextPoll(t, app)
	require.True(t, app.runtimeOf(passKey).hold.Held())
	assert.Empty(t, passComments(client), "a deployment runs the pull request's code")

	client.mu.Lock()
	client.review = sampleThreads()
	client.mu.Unlock()
	pump(t, app, app.loadReview(passKey, false))
	assert.Len(t, passComments(client), 1, "once the hold clears it is posted")
}

func TestUnreadThreadsAreReadBeforeTheChecksPassedComment(t *testing.T) {
	app, client := passApp(t)
	send(t, app, press("P"))
	// A reading of green checks, as applyWatch records it, on a pull request
	// whose threads this session has not read.
	entry := app.mutate(passKey)
	entry.holdKnown = false
	entry.headOID, entry.rollup = "abc", model.StatusSuccess

	cmd := app.postOnPass(passKey, "abc", model.StatusSuccess)
	require.NotNil(t, cmd, "not knowing who has commented asks rather than posting")
	assert.Empty(t, passComments(client))

	pump(t, app, cmd)
	assert.Len(t, passComments(client), 1, "the read's reply posts it")
}

func TestStoppingTheWatchDisarmsTheChecksPassedComment(t *testing.T) {
	app, client := passApp(t)
	send(t, app, press("P"))
	send(t, app, press("w"))
	assert.False(t, app.state.PostOnPass(passKey.String()))

	send(t, app, press("w"))
	assert.False(t, app.state.PostOnPass(passKey.String()), "watching again does not bring it back")
	setRollup(client, "abc", model.StatusSuccess)
	nextPoll(t, app)
	assert.Empty(t, passComments(client))
}

func TestAFailedChecksPassedCommentIsTriedAgain(t *testing.T) {
	app, client := passApp(t)
	send(t, app, press("P"))
	client.mu.Lock()
	client.commentErr = errors.New("github 502")
	client.mu.Unlock()

	setRollup(client, "abc", model.StatusSuccess)
	nextPoll(t, app)
	assert.Empty(t, app.state.Get(passKey.String()).LastPassCommentHead, "a comment that failed is not recorded")
	assert.False(t, app.busy())

	client.mu.Lock()
	client.commentErr = nil
	client.mu.Unlock()
	nextPoll(t, app)
	assert.Len(t, passComments(client), 1)
}

func TestADryRunRecordsTheChecksPassedCommentWithoutPostingIt(t *testing.T) {
	app, client := passApp(t)
	app.homeCfg.Herdr.DryRun = true
	send(t, app, press("P"))

	setRollup(client, "abc", model.StatusSuccess)
	nextPoll(t, app)
	assert.Empty(t, passComments(client))
	assert.Equal(t, "abc", app.state.Get(passKey.String()).LastPassCommentHead)
	activity := app.runtimeOf(passKey).activity
	require.NotEmpty(t, activity)
	assert.Equal(t, "dry run: would have posted /deploy staging for abc", activity[len(activity)-1].text)
}

func TestTheWatchSectionSaysWhatPWillPost(t *testing.T) {
	app, client := passApp(t)
	send(t, app, press("P"))
	pr, ok := app.selectedPR()
	require.True(t, ok)

	row, shown := app.onPassRow(app.watchFactsOf(pr))
	require.True(t, shown)
	assert.Equal(t, "when checks pass: post /deploy staging", row.text)

	setRollup(client, "abc", model.StatusSuccess)
	nextPoll(t, app)
	row, _ = app.onPassRow(app.watchFactsOf(pr))
	assert.Equal(t, "when checks pass: post /deploy staging · posted for abc", row.text)
}
