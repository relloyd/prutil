package ui

import (
	"errors"
	"fmt"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/relloyd/prutil/internal/desktop"
	"github.com/relloyd/prutil/internal/home"
	"github.com/relloyd/prutil/internal/model"
)

// pr7 is the sample pull request that starts out with changes requested.
var pr7 = model.Key{Repo: "relloyd/other", Number: 7}

// withDecision returns the sample list with one pull request's review
// decision changed.
func withDecision(key model.Key, decision model.ReviewDecision) []model.PullRequest {
	prs := samplePRs()
	for i := range prs {
		if prs[i].Key() == key {
			prs[i].ReviewDecision = decision
		}
	}
	return prs
}

// reload delivers a freshly loaded open list asked for at the app's current
// time, runs whatever it asks for, and returns the status lines it produced.
func reload(t *testing.T, app *App, prs []model.PullRequest) []statusMsg {
	t.Helper()
	return statuses(drain(send(t, app, prsMsg{gen: app.gen, view: viewOpen, prs: prs, at: app.now()})))
}

// statuses picks the status lines out of a drained command.
func statuses(msgs []tea.Msg) []statusMsg {
	var out []statusMsg
	for _, msg := range msgs {
		if s, ok := msg.(statusMsg); ok {
			out = append(out, s)
		}
	}
	return out
}

// pollOnce runs one notification read, as the tick would, applies its reply,
// and returns the status lines that produced.
func pollOnce(t *testing.T, app *App) []statusMsg {
	t.Helper()
	var out []statusMsg
	for _, msg := range drain(send(t, app, listReadTickMsg{seq: app.listReadSeq})) {
		reply, ok := msg.(notifyPollMsg)
		require.True(t, ok, "a tick is answered by a read, got %T", msg)
		out = append(out, statuses(drain(send(t, app, reply)))...)
	}
	return out
}

func TestTheFirstListIsWhereThingsStandNotNews(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)

	assert.Empty(t, notifierOf(t, app).notifications(),
		"#42 was approved before prutil looked, and the reader can see that on screen")
	assert.True(t, app.runtimeOf(model.Key{Repo: "relloyd/prutil", Number: 42}).hasFacts)
}

func TestARefreshThatFindsAnApprovalRaisesANotification(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)

	lines := reload(t, app, withDecision(pr7, model.ReviewApproved))

	shown := notifierOf(t, app).notifications()
	require.Len(t, shown, 1)
	assert.Equal(t, desktop.Notification{Title: "relloyd/other#7 approved", Body: "Rework the config loader"}, shown[0])
	assert.Contains(t, lines, statusMsg("relloyd/other#7 approved"))

	reload(t, app, withDecision(pr7, model.ReviewApproved))
	assert.Len(t, notifierOf(t, app).notifications(), 1, "staying approved is not news")
}

func TestAnApprovalLostAndRegainedIsNewsBothTimes(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)

	reload(t, app, withDecision(pr7, model.ReviewApproved))
	reload(t, app, withDecision(pr7, model.ReviewRequired))
	reload(t, app, withDecision(pr7, model.ReviewApproved))

	assert.Len(t, notifierOf(t, app).notifications(), 2)
}

func TestAFirstApprovalInARepositoryWithoutRulesIsNews(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	third := model.Key{Repo: "relloyd/third", Number: 9}

	prs := samplePRs()
	prs[2].Approvals = 1
	reload(t, app, prs)

	shown := notifierOf(t, app).notifications()
	require.Len(t, shown, 1)
	assert.Equal(t, third.String()+" approved", shown[0].Title)

	prs[2].Approvals = 2
	reload(t, app, prs)
	assert.Len(t, notifierOf(t, app).notifications(), 1, "a second approval is not a second notification")
}

func TestATurnedOffNotificationStaysQuietAndDoesNotRaiseOldNewsLater(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	app.homeCfg.Notifications.Set(home.NotifyApproved, false)

	lines := reload(t, app, withDecision(pr7, model.ReviewApproved))
	assert.Empty(t, notifierOf(t, app).notifications())
	assert.Empty(t, lines)

	app.homeCfg.Notifications.Set(home.NotifyApproved, true)
	reload(t, app, withDecision(pr7, model.ReviewApproved))
	assert.Empty(t, notifierOf(t, app).notifications(), "the approval was already known when it was turned on")
}

func TestThePollReadsEveryOpenPullRequestNotOnlyTheWatchedOnes(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	require.True(t, app.listReadWaiting, "loading the list started the wait for the first read")

	snap := client.snapshots["PR_7"]
	snap.ReviewDecision = model.ReviewChangesRequested
	client.snapshots["PR_7"] = snap
	pollOnce(t, app)
	require.Equal(t, [][]string{{"PR_42", "PR_7", "PR_9"}}, client.batches(),
		"one request for every open pull request, none of them watched")
	assert.Empty(t, notifierOf(t, app).notifications())

	snap.ReviewDecision = model.ReviewApproved
	client.snapshots["PR_7"] = snap
	advance(app, time.Minute)
	lines := pollOnce(t, app)

	shown := notifierOf(t, app).notifications()
	require.Len(t, shown, 1)
	assert.Equal(t, "relloyd/other#7 approved", shown[0].Title)
	assert.Equal(t, "Rework the config loader", shown[0].Body, "the title comes from the list")
	assert.Contains(t, lines, statusMsg("relloyd/other#7 approved"))
	assert.True(t, app.listReadWaiting, "the reply waits for the next read")
}

func TestAPollThatSaysNothingNewRaisesNothing(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	// The fake's snapshots carry no review decision, and #42 is approved in
	// the list. Only the approval matters: losing it is not news.
	pollOnce(t, app)
	pollOnce(t, app)

	assert.Len(t, client.batches(), 2)
	assert.Empty(t, notifierOf(t, app).notifications())
}

func TestNothingIsReadWhileEveryNotificationIsOff(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	app.homeCfg.Notifications.Set(home.NotifyApproved, false)

	assert.Nil(t, send(t, app, listReadTickMsg{seq: app.listReadSeq}), "the outstanding wait ends the run")
	assert.False(t, app.listReadWaiting)
	assert.Empty(t, client.batches())

	assert.Nil(t, app.scheduleListRead(), "and nothing starts another")
	assert.Nil(t, app.noticeAfterLoad(prsMsg{view: viewOpen, prs: samplePRs(), at: app.now()}),
		"a list load does not restart it either")
}

func TestTurningANotificationBackOnStartsTheReadsAgain(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	openSettingsPane(t, app)
	send(t, app, press("space"))
	require.False(t, app.listReadWaiting, "turning the only one off ends the wait")

	cmd := send(t, app, press("space"))
	assert.NotNil(t, cmd, "turning one on schedules the next read")
	assert.True(t, app.listReadWaiting)

	assert.Nil(t, send(t, app, press("space")), "turning it off schedules nothing")
	assert.False(t, app.listReadWaiting)
}

func TestThereIsOnlyEverOneReadOutstanding(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	stale := app.listReadSeq
	app.scheduleListRead()
	assert.Nil(t, send(t, app, listReadTickMsg{seq: stale}), "a wait since replaced reads nothing")

	cmd := send(t, app, listReadTickMsg{seq: app.listReadSeq})
	require.NotNil(t, cmd)
	assert.True(t, app.notifyReading, "the read is outstanding until its reply lands")

	app.scheduleListRead()
	assert.Nil(t, send(t, app, listReadTickMsg{seq: app.listReadSeq}), "a tick while it is out reads nothing more")
	drain(cmd)
	assert.Len(t, client.batches(), 1)
}

func TestWithoutANotifierNothingIsRead(t *testing.T) {
	client := newFakeClient(samplePRs(), sampleChecks())
	app := New(Config{Client: client, Home: fastWatch(), Now: func() time.Time { return testNow }})
	send(t, app, tea.WindowSizeMsg{Width: 120, Height: 40})
	send(t, app, prsMsg{gen: app.gen, prs: samplePRs()})

	assert.False(t, app.listReadWaiting)
	assert.Nil(t, send(t, app, listReadTickMsg{seq: app.listReadSeq}))
	assert.Empty(t, client.batches())

	lines := reload(t, app, withDecision(pr7, model.ReviewApproved))
	assert.Contains(t, lines, statusMsg("relloyd/other#7 approved"), "the status line still says so")
}

func TestAFailedReadIsKeptForTheSettingsAndTriedAgain(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	client.watchErr = errors.New("could not resolve host")

	lines := pollOnce(t, app)
	assert.Empty(t, lines, "a laptop without a network is not told so every two minutes")
	assert.EqualError(t, app.notifyErr, "could not resolve host")
	assert.True(t, app.listReadWaiting, "the next read is already waited for")

	client.watchErr = nil
	pollOnce(t, app)
	assert.NoError(t, app.notifyErr)
}

func TestTheWatcherReadingsRaiseNotificationsToo(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	send(t, app, press("j"))
	send(t, app, press("w"))
	require.True(t, app.armed(pr7))

	snap := client.snapshots["PR_7"]
	snap.ReviewDecision = model.ReviewApproved
	snap.Key = pr7
	drain(send(t, app, watchSnapshotMsg{keys: []model.Key{pr7}, snaps: []model.Snapshot{snap}, at: testNow}))

	shown := notifierOf(t, app).notifications()
	require.Len(t, shown, 1)
	assert.Equal(t, "relloyd/other#7 approved", shown[0].Title)
	assert.Contains(t, activityTexts(app, pr7), "approved; desktop notification raised",
		"a watched pull request's WATCH activity says so")
}

func TestAnUnwatchedPullRequestGainsNoWatchActivityFromANotification(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	reload(t, app, withDecision(pr7, model.ReviewApproved))

	assert.Empty(t, activityTexts(app, pr7))
	send(t, app, press("j"))
	assert.False(t, app.hasWatchSection(samplePRs()[1]), "no WATCH section appears for it")
}

func TestAReadingOlderThanTheOneHeldIsIgnored(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	asked := app.now()

	// The poll sees the approval first...
	snap := client.snapshots["PR_7"]
	snap.ReviewDecision = model.ReviewApproved
	client.snapshots["PR_7"] = snap
	advance(app, time.Minute)
	pollOnce(t, app)
	require.Len(t, notifierOf(t, app).notifications(), 1)

	// ...and then a list asked for before it lands, still showing changes
	// requested. Applying it would let the next poll announce the approval
	// again.
	drain(send(t, app, prsMsg{gen: app.gen, view: viewOpen, prs: samplePRs(), at: asked}))
	assert.True(t, app.runtimeOf(pr7).facts.approved)

	advance(app, time.Minute)
	pollOnce(t, app)
	assert.Len(t, notifierOf(t, app).notifications(), 1)
}

func TestManyChangesAtOnceAreSummedUpInOneNotification(t *testing.T) {
	prs := make([]model.PullRequest, 0, maxToasts+1)
	for i := range maxToasts + 1 {
		prs = append(prs, model.PullRequest{
			Repo: "acme/widgets", Number: i + 1, NodeID: fmt.Sprintf("PR_%d", i+1),
			Title: fmt.Sprintf("Change %d", i+1), ReviewDecision: model.ReviewRequired,
		})
	}
	client := newFakeClient(prs, nil)
	notifier := &fakeNotifier{}
	app := New(Config{Client: client, Home: fastWatch(), Notifier: notifier, Now: func() time.Time { return testNow }})
	send(t, app, tea.WindowSizeMsg{Width: 120, Height: 40})
	send(t, app, prsMsg{gen: app.gen, prs: prs})

	approved := make([]model.PullRequest, len(prs))
	copy(approved, prs)
	for i := range approved {
		approved[i].ReviewDecision = model.ReviewApproved
	}
	lines := reload(t, app, approved)

	shown := notifier.notifications()
	require.Len(t, shown, 1)
	assert.Equal(t, "4 pull requests changed", shown[0].Title)
	assert.Equal(t, "acme/widgets#1 approved, acme/widgets#2 approved, acme/widgets#3 approved, acme/widgets#4 approved", shown[0].Body)
	require.Len(t, lines, 1)
	assert.Contains(t, string(lines[0]), "4 pull requests changed: acme/widgets#1 approved")
}

func TestAFewChangesAtOnceEachGetANotification(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	prs := withDecision(pr7, model.ReviewApproved)
	prs[2].Approvals = 1

	lines := reload(t, app, prs)
	assert.Len(t, notifierOf(t, app).notifications(), 2)
	assert.Contains(t, lines, statusMsg("2 pull requests changed: relloyd/other#7 approved, relloyd/third#9 approved"))
}

func TestANotificationThatCannotBeShownIsReported(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	notifierOf(t, app).err = errors.New("no notification server")

	lines := reload(t, app, withDecision(pr7, model.ReviewApproved))
	assert.Contains(t, lines, statusMsg("could not show a desktop notification: no notification server"))
}

func activityTexts(app *App, key model.Key) []string {
	var out []string
	for _, a := range app.runtimeOf(key).activity {
		out = append(out, a.text)
	}
	return out
}

// withRollup returns the sample list with one pull request's head commit and
// check rollup changed.
func withRollup(key model.Key, head string, rollup model.Status) []model.PullRequest {
	prs := samplePRs()
	for i := range prs {
		if prs[i].Key() == key {
			prs[i].HeadOID, prs[i].Rollup = head, rollup
		}
	}
	return prs
}

func TestChecksPassingIsNewsOncePerCommit(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	app.homeCfg.Notifications.Set(home.NotifyChecksPassed, true)
	reload(t, app, withRollup(pr7, "c1", model.StatusFailure))

	lines := reload(t, app, withRollup(pr7, "c1", model.StatusSuccess))
	shown := notifierOf(t, app).notifications()
	require.Len(t, shown, 1)
	assert.Equal(t, "relloyd/other#7 checks passed", shown[0].Title)
	assert.Contains(t, lines, statusMsg("relloyd/other#7 checks passed"))

	reload(t, app, withRollup(pr7, "c1", model.StatusSuccess))
	assert.Len(t, notifierOf(t, app).notifications(), 1, "staying green is not news")

	reload(t, app, withRollup(pr7, "c2", model.StatusSuccess))
	assert.Len(t, notifierOf(t, app).notifications(), 2,
		"a new commit whose checks passed between two readings is news, though it was never seen running")
}

func TestChecksPassingIsQuietUntilItIsTurnedOn(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	require.False(t, app.homeCfg.Notifications.Enabled(home.NotifyChecksPassed), "it is off by default")

	reload(t, app, withRollup(pr7, "c1", model.StatusPending))
	reload(t, app, withRollup(pr7, "c1", model.StatusSuccess))
	assert.Empty(t, notifierOf(t, app).notifications())
}
