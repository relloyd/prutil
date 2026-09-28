package ui

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/relloyd/prutil/internal/model"
)

// autoWatchSince is when auto-watch was switched on in these tests.
var autoWatchSince = testNow.Add(-time.Hour)

// newAutoWatchApp builds an app with auto-watch on since autoWatchSince,
// knowing who the reader is, and a list limit the loads stay under.
func newAutoWatchApp(t *testing.T) (*App, *fakeClient) {
	t.Helper()
	app, client, _ := newTestApp(t, 120, 40)
	app.homeCfg.Watch.AutoWatch = true
	app.limit = 100
	app.viewer = "relloyd"
	app.state.StartAutoWatch(autoWatchSince)
	return app, client
}

// newPR is a pull request of the reader's opened after auto-watch was
// switched on.
func newPR(number int) model.PullRequest {
	return model.PullRequest{
		Repo: "relloyd/prutil", Number: number, NodeID: fmt.Sprintf("PR_N%d", number),
		Title: "A new idea", Author: "relloyd", HeadRef: fmt.Sprintf("feat/n%d", number), BaseRef: "main",
		CreatedAt: autoWatchSince.Add(time.Duration(number) * time.Minute),
		UpdatedAt: testNow, Rollup: model.StatusPending,
	}
}

// loadOwn delivers an open list load in which every pull request is one the
// list's own search found. It returns nothing: the command it produces waits
// on a timer, which a test must not run.
func loadOwn(t *testing.T, app *App, prs ...model.PullRequest) {
	t.Helper()
	own := make(map[model.Key]bool, len(prs))
	for _, pr := range prs {
		own[pr.Key()] = true
	}
	send(t, app, prsMsg{gen: app.gen, view: viewOpen, prs: prs, own: own, at: app.now()})
}

func TestANewPullRequestOfYoursIsWatchedAutomatically(t *testing.T) {
	app, _ := newAutoWatchApp(t)
	pr := newPR(50)

	loadOwn(t, app, pr)

	assert.True(t, app.armed(pr.Key()))
	assert.Contains(t, activityTexts(app, pr.Key()), "started watching automatically: a new pull request of yours")
	stored, err := app.store.LoadState()
	require.NoError(t, err)
	assert.True(t, stored.Armed(pr.Key().String()), "and the watch survives a restart")
	assert.Contains(t, lines(app)[0], "new PR watching", "the header says why it is watched, in the words the settings pane uses")
}

func TestWhatWasAlreadyOpenWhenAutoWatchWasSwitchedOnIsLeftAlone(t *testing.T) {
	app, _ := newAutoWatchApp(t)
	old := newPR(51)
	old.CreatedAt = autoWatchSince.Add(-time.Minute)

	loadOwn(t, app, old)

	assert.False(t, app.armed(old.Key()))
}

func TestOnlyThePullRequestsTheReaderOpenedAreAutoWatched(t *testing.T) {
	theirs := newPR(52)
	theirs.Author = "alice"
	nobody := newPR(53)
	nobody.Author = ""

	cases := []struct {
		name string
		pr   model.PullRequest
	}{
		{name: "somebody else's, which a custom -query can list", pr: theirs},
		{name: "one whose author GitHub has lost", pr: nobody},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, _ := newAutoWatchApp(t)

			loadOwn(t, app, tc.pr)

			assert.False(t, app.armed(tc.pr.Key()))
		})
	}
}

func TestAnAdoptedPullRequestIsNotAutoWatched(t *testing.T) {
	app, _ := newAutoWatchApp(t)
	adopted := newPR(54)

	send(t, app, prsMsg{gen: app.gen, view: viewOpen, prs: []model.PullRequest{adopted}, own: map[model.Key]bool{}})

	assert.False(t, app.armed(adopted.Key()),
		"only what the list's own search found; watching an adopted one asks first")
}

func TestNothingIsArmedUntilPrutilKnowsWhoTheReaderIs(t *testing.T) {
	app, _ := newAutoWatchApp(t)
	app.viewer = ""
	pr := newPR(55)

	loadOwn(t, app, pr)

	assert.False(t, app.armed(pr.Key()), "an author prutil cannot compare against is not known to be the reader")
	assert.Empty(t, app.state.AutoWatch.Considered, "and nothing is decided about it, so a later load still can")
}

func TestStoppingAnAutoWatchedPullRequestSticks(t *testing.T) {
	app, _ := newAutoWatchApp(t)
	pr := newPR(56)
	loadOwn(t, app, pr)
	require.True(t, app.armed(pr.Key()))

	selectKey(t, app, pr.Key())
	send(t, app, press("w"))
	require.False(t, app.armed(pr.Key()))
	loadOwn(t, app, pr)

	assert.False(t, app.armed(pr.Key()), "the next load must not undo the reader's w")
}

func TestAPullRequestTheReaderWatchedFirstIsStillConsidered(t *testing.T) {
	app, _ := newAutoWatchApp(t)
	pr := newPR(57)
	app.state.SetArmed(pr.Key().String(), true)

	loadOwn(t, app, pr)
	app.state.SetArmed(pr.Key().String(), false)
	loadOwn(t, app, pr)

	assert.False(t, app.armed(pr.Key()), "stopping it later is not undone either")
}

func TestADraftWaitsUntilItIsReadyForReview(t *testing.T) {
	app, _ := newAutoWatchApp(t)
	draft := newPR(58)
	draft.IsDraft = true

	loadOwn(t, app, draft)
	require.False(t, app.armed(draft.Key()), "a draft's checks failing is usually the author still pushing")

	ready := draft
	ready.IsDraft = false
	loadOwn(t, app, ready)

	assert.True(t, app.armed(ready.Key()), "and it is watched once it is marked ready")
}

func TestDraftsAreWatchedWhenTheReaderAsksForThem(t *testing.T) {
	app, _ := newAutoWatchApp(t)
	app.homeCfg.Watch.AutoWatchDrafts = true
	draft := newPR(59)
	draft.IsDraft = true

	loadOwn(t, app, draft)

	assert.True(t, app.armed(draft.Key()))
}

func TestOneLoadArmsAtMostABurstOfPullRequests(t *testing.T) {
	app, _ := newAutoWatchApp(t)
	var prs []model.PullRequest
	for n := 60; n < 67; n++ {
		prs = append(prs, newPR(n))
	}

	loadOwn(t, app, prs...)
	assert.Equal(t, autoWatchBurst, app.state.ArmedCount(),
		"a stack of pull requests opened at once should not be a burst of agents")

	loadOwn(t, app, prs...)
	assert.Equal(t, len(prs), app.state.ArmedCount(), "the rest wait for the next load")
}

func TestAPullRequestThatClosedIsForgottenOnceTheListIsWhole(t *testing.T) {
	app, _ := newAutoWatchApp(t)
	pr := newPR(70)
	loadOwn(t, app, pr)
	require.True(t, app.state.AutoWatch.Seen(pr.Key().String()))

	loadOwn(t, app, newPR(71))

	assert.False(t, app.state.AutoWatch.Seen(pr.Key().String()),
		"GitHub does not reuse a number, so a closed one never needs remembering")
}

func TestAListCutShortByTheLimitForgetsNothing(t *testing.T) {
	app, _ := newAutoWatchApp(t)
	pr := newPR(72)
	loadOwn(t, app, pr)
	app.limit = 1

	loadOwn(t, app, newPR(73))

	assert.True(t, app.state.AutoWatch.Seen(pr.Key().String()),
		"a pull request past the limit may still be open, and may be one the reader stopped watching")
}

func TestSwitchingAutoWatchOnMeasuresNewFromThatMoment(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	app.viewer = "relloyd"
	app.limit = 100
	before := newPR(80)
	before.CreatedAt = testNow.Add(-time.Minute)

	app.homeCfg.Watch.AutoWatch = true
	app.setAutoWatch()
	loadOwn(t, app, before)

	require.NotNil(t, app.state.AutoWatch)
	assert.Equal(t, testNow, app.state.AutoWatch.Since)
	assert.False(t, app.armed(before.Key()), "opened before it was switched on")

	app.homeCfg.Watch.AutoWatch = false
	app.setAutoWatch()
	assert.Nil(t, app.state.AutoWatch, "switching it off forgets it, so switching it on again starts afresh")
}

func TestAutoWatchSwitchedOnInTheFileStartsFromTheFirstLoad(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	app.viewer = "relloyd"
	app.homeCfg.Watch.AutoWatch = true
	earlier := newPR(81)
	earlier.CreatedAt = testNow.Add(-time.Minute)

	loadOwn(t, app, earlier)

	require.NotNil(t, app.state.AutoWatch)
	assert.Equal(t, testNow, app.state.AutoWatch.Since)
	assert.False(t, app.armed(earlier.Key()))
}

func TestTheTickReReadsTheOpenList(t *testing.T) {
	app, client := newAutoWatchApp(t)
	app.scheduleAutoWatch()
	before := client.listCalls

	msgs := drain(send(t, app, autoWatchTickMsg{seq: app.autoWatchSeq}))

	assert.Equal(t, before+1, client.listCalls)
	var loaded bool
	for _, msg := range msgs {
		_, ok := msg.(prsMsg)
		loaded = loaded || ok
	}
	assert.True(t, loaded)
}

func TestAStaleTickIsDropped(t *testing.T) {
	app, client := newAutoWatchApp(t)
	app.scheduleAutoWatch()
	stale := app.autoWatchSeq
	app.scheduleAutoWatch()

	cmd := send(t, app, autoWatchTickMsg{seq: stale})

	assert.Nil(t, cmd, "a load since replaced the wait it belonged to")
	assert.Zero(t, client.listCalls, "nothing was read")
}

func TestATickIsIgnoredOnceAutoWatchIsOff(t *testing.T) {
	app, _ := newAutoWatchApp(t)
	app.scheduleAutoWatch()
	seq := app.autoWatchSeq
	app.homeCfg.Watch.AutoWatch = false

	assert.Nil(t, send(t, app, autoWatchTickMsg{seq: seq}))
}

func TestTheLoadAsksWhoTheReaderIsOnlyWhileAutoWatchNeedsIt(t *testing.T) {
	app, client := newAutoWatchApp(t)
	app.viewer = ""
	client.viewer = "relloyd"

	msg, ok := app.loadOpen(app.gen)().(prsMsg)
	require.True(t, ok)
	assert.Equal(t, "relloyd", msg.viewer)

	send(t, app, msg)
	app.loadOpen(app.gen)()
	assert.Equal(t, 1, client.viewerCalls, "once known, it is not asked again")

	app.homeCfg.Watch.AutoWatch = false
	app.viewer = ""
	app.loadOpen(app.gen)()
	assert.Equal(t, 1, client.viewerCalls, "and it is not asked at all while auto-watch is off")
}
