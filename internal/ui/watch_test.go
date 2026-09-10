package ui

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/relloyd/prutil/internal/handoff"
	"github.com/relloyd/prutil/internal/home"
	"github.com/relloyd/prutil/internal/model"
)

func TestWatchingMarksTheRowAndCountsInTheHeader(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)

	send(t, app, press("w"))
	screen := plain(app.render())

	assert.Contains(t, screen, watchGlyph+" #42", "the armed pull request is marked on its row")
	assert.Contains(t, headerLine(app), watchGlyph+" 1")
}

func TestWatchingIsRememberedForTheNextRun(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)

	send(t, app, press("w"))

	state, err := app.store.LoadState()
	require.NoError(t, err)
	assert.True(t, state.Armed("relloyd/prutil#42"))
}

func TestPressingWatchAgainStopsWatching(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)

	send(t, app, press("w"))
	cmd := send(t, app, press("w"))
	require.NotNil(t, cmd)

	assert.Equal(t, statusMsg("stopped watching relloyd/prutil#42"), cmd())
	assert.NotContains(t, plain(app.render()), watchGlyph)
}

func TestWatchingFollowsTheCursorRatherThanTheWholeList(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)

	send(t, app, press("j"))
	send(t, app, press("w"))

	assert.True(t, app.state.Armed("relloyd/other#7"))
	assert.False(t, app.state.Armed("relloyd/prutil#42"))
}

func TestWatchingSaysWhyItCannotWhenThereIsNoApplicationDirectory(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	app.store, app.storeErr = nil, errors.New("no home directory")

	cmd := send(t, app, press("w"))
	require.NotNil(t, cmd)
	assert.Equal(t, statusMsg("cannot remember what is watched: no home directory"), cmd())
}

func TestHandingOverSendsOnlyTheThreadsStillWaitingOnTheViewer(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	dispatcher := dispatcherOf(t, app)

	handOver(t, app)

	assert.Equal(t, 1, client.reviewCalls)
	reqs := dispatcher.requests()
	require.Len(t, reqs, 1)

	// Four threads came back: two waiting on a reviewer's word, one the viewer
	// already answered, and one resolved.
	assert.Equal(t, 2, reqs[0].UnresolvedCount)
	assert.Equal(t, 2, reqs[0].NewCount)
	assert.Equal(t, map[string]string{"T1": "C1", "T2": "C2"}, reqs[0].Threads)
	assert.Equal(t, "relloyd/prutil#42", reqs[0].PR.Key().String())
	assert.True(t, reqs[0].AllowProvision, "W is the reader's consent to provision when no agent exists")
}

func TestManualDiscoveryUsesTheAutomaticNonProvisioningHandoff(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	dispatcher := dispatcherOf(t, app)
	dispatcher.result = handoff.Result{Outcome: home.OutcomeSent, Target: "w2:p1", Kind: "claude"}

	notifyNewFeedback(t, app)

	assert.Equal(t, 1, client.reviewCalls)
	reqs := dispatcher.requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, 2, reqs[0].UnresolvedCount)
	assert.Equal(t, 2, reqs[0].NewCount)
	assert.False(t, reqs[0].AllowProvision, "N must retain automatic handoff safety")
	assert.False(t, app.state.Armed("relloyd/prutil#42"), "N works without arming the pull request")
	assert.Equal(t, map[string]string{"T1": "C1", "T2": "C2"},
		app.state.Get("relloyd/prutil#42").NotifiedThreads)
	assert.Equal(t, home.OutcomeSent, lastHandoff(t, app).Outcome)
	assert.Contains(t, plain(app.render()), "manual discovery")
}

func TestManualDiscoverySkipsPreviouslyHandedFeedback(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	dispatcher := dispatcherOf(t, app)
	dispatcher.result = handoff.Result{Outcome: home.OutcomeSent, Target: "w2:p1", Kind: "claude"}

	notifyNewFeedback(t, app)
	notifyNewFeedback(t, app)

	assert.Equal(t, 2, client.reviewCalls, "each manual discovery reads the real current feedback")
	assert.Len(t, dispatcher.requests(), 1, "the second read finds no feedback that is new to prutil")
	assert.Equal(t, "no new review feedback on relloyd/prutil#42", app.status)
}

func TestManualDiscoveryRejectsTheClosedView(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	app.active = viewClosed
	app.views[viewClosed].prs = sampleClosedPRs()

	cmd := send(t, app, press("N"))
	require.NotNil(t, cmd)
	assert.Equal(t, statusMsg("new-feedback notification is available only for open pull requests"), cmd())
	assert.Zero(t, client.reviewCalls)
}

func TestManualDiscoveryDoesNotStartAnOverlappingReviewRead(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)

	first := send(t, app, press("N"))
	require.NotNil(t, first)
	assert.True(t, app.busy(), "a precise review read must keep the progress indicator active")
	drain(first)
	assert.Equal(t, 1, client.reviewCalls, "the first command reaches GitHub")

	second := send(t, app, press("N"))
	require.NotNil(t, second)

	assert.Equal(t, statusMsg("already reading review feedback on relloyd/prutil#42"), second())
	assert.Equal(t, 1, client.reviewCalls, "the second press does not start another query")
}

func TestManualHandoffDoesNotRaceManualDiscovery(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)

	first := send(t, app, press("N"))
	require.NotNil(t, first)
	cmd := send(t, app, press("W"))
	require.NotNil(t, cmd)

	assert.Equal(t, statusMsg("already reading review feedback on relloyd/prutil#42"), cmd())
	assert.Empty(t, dispatcherOf(t, app).requests())
	assert.Zero(t, client.reviewCalls, "the blocked handoff cannot start a second review read")
}

func TestThreadsAlreadyHandedOverAreNotCountedAsNewAgain(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	dispatcher := dispatcherOf(t, app)
	dispatcher.result = handoff.Result{Outcome: home.OutcomeSent, Target: "w2:p1", Kind: "claude"}

	handOver(t, app)
	handOver(t, app)

	reqs := dispatcher.requests()
	require.Len(t, reqs, 2)
	assert.Equal(t, 2, reqs[1].UnresolvedCount, "the feedback is still open")
	assert.Equal(t, 0, reqs[1].NewCount, "but none of it is new, so an agent is not asked twice")
}

func TestAFailedHandoffDoesNotRememberTheThreadsAsHandedOver(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	dispatcher := dispatcherOf(t, app)
	dispatcher.result = handoff.Result{Outcome: home.OutcomeNoAgent}
	dispatcher.err = handoff.ErrNoAgent

	handOver(t, app)

	assert.Empty(t, app.state.Get("relloyd/prutil#42").NotifiedThreads)
}

func TestADryRunIsNotRememberedEitherBecauseNothingWasSent(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	dispatcher := dispatcherOf(t, app)
	dispatcher.dry = true
	dispatcher.result = handoff.Result{Outcome: home.OutcomeDryRun, Target: "w2:p1", Kind: "claude"}

	handOver(t, app)

	assert.Empty(t, app.state.Get("relloyd/prutil#42").NotifiedThreads)
	assert.Contains(t, app.status, "dry run: relloyd/prutil#42 would go to claude w2:p1")
}

func TestAPullRequestWithNothingOpenIsNotHandedToAnybody(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	client.review.Threads = nil
	dispatcher := dispatcherOf(t, app)

	handOver(t, app)

	assert.Empty(t, dispatcher.requests(), "there is no point waking an agent for nothing")
	assert.Equal(t, "no open review feedback on relloyd/prutil#42", app.status)
}

func TestTheStatusLineNamesTheAgentAndTheCounts(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	dispatcher := dispatcherOf(t, app)
	dispatcher.result = handoff.Result{
		Outcome: home.OutcomeSent, Target: "w2:p1", Kind: "claude", Waited: 90 * time.Second,
	}

	handOver(t, app)

	assert.Equal(t, "handed relloyd/prutil#42 to claude w2:p1 · 2 of 2 open threads new · waited 1m30s", app.status)
}

func TestNoAgentIsExplainedRatherThanReportedAsAFailure(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	dispatcher := dispatcherOf(t, app)
	dispatcher.result = handoff.Result{Outcome: home.OutcomeNoAgent}
	dispatcher.err = handoff.ErrNoAgent

	handOver(t, app)

	assert.Equal(t,
		"relloyd/prutil#42: 2 of 2 open threads new, but no agent is checked out in relloyd/prutil",
		app.status)
}

func TestGitHubRefusingTheReviewThreadsIsReportedAndLogged(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	client.reviewErr = errors.New("api rate limit exceeded")
	dispatcher := dispatcherOf(t, app)

	handOver(t, app)

	assert.Empty(t, dispatcher.requests())
	assert.Contains(t, app.status, "api rate limit exceeded")
	assert.Equal(t, home.OutcomeFailed, lastHandoff(t, app).Outcome)
}

func TestEveryHandoffAttemptIsLoggedWhateverBecameOfIt(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	dispatcher := dispatcherOf(t, app)
	dispatcher.result = handoff.Result{
		Outcome: home.OutcomeSent, Target: "w2:p1", Kind: "claude",
		Dir: "/work/prutil", Prompt: "/pr-triage https://example.test",
		Provisioned: true, Workspace: "w2", Tab: "w2:t1",
	}

	handOver(t, app)

	logged := lastHandoff(t, app)
	assert.Equal(t, "relloyd/prutil#42", logged.PR)
	assert.Equal(t, "https://github.com/relloyd/prutil/pull/42", logged.URL)
	assert.Equal(t, home.OutcomeSent, logged.Outcome)
	assert.Equal(t, "w2:p1", logged.Target)
	assert.Equal(t, "/pr-triage https://example.test", logged.Prompt)
	assert.True(t, logged.Provisioned)
	assert.Equal(t, "w2", logged.Workspace)
	assert.Equal(t, "w2:t1", logged.Tab)
}

func TestASecondHandoffIsRefusedWhileTheFirstIsStillInFlight(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)

	// The first press marks the pull request in flight before its command has
	// had a chance to run, which is exactly the window a held-down key finds.
	require.NotNil(t, send(t, app, press("W")))
	cmd := send(t, app, press("W"))
	require.NotNil(t, cmd)

	assert.Equal(t, statusMsg("already handing relloyd/prutil#42 over"), cmd())
}

func TestHandingOverSaysWhyItCannotWhenHerdrIsNotThere(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	app.hand, app.handErr = nil, errors.New("no herdr server is answering")

	cmd := send(t, app, press("W"))
	require.NotNil(t, cmd)
	assert.Equal(t, statusMsg("cannot reach herdr: no herdr server is answering"), cmd())
}

func TestTheSpinnerRunsWhileAHandoffWaitsForAnAgent(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	require.False(t, app.busy())

	send(t, app, press("W"))
	assert.True(t, app.busy(), "a wait of minutes must not look like a hang")
}

func TestFeedbackIsWhateverIsUnresolvedAndNotTheViewersOwnLastWord(t *testing.T) {
	threads := sampleThreads().Threads

	got := model.Feedback(threads, "relloyd")
	require.Len(t, got, 2)
	assert.Equal(t, "T1", got[0].ID)
	assert.Equal(t, "T2", got[1].ID)
}

// handOver presses the handoff key and settles the reply the way the runtime
// would. The command and the status line announcing it are batched together,
// so a test that fed the batch back in order would end on "reading the review
// threads" rather than on what became of them.
func handOver(t *testing.T, app *App) {
	t.Helper()

	for _, msg := range drain(send(t, app, press("W"))) {
		got, ok := msg.(handoffMsg)
		if !ok {
			continue
		}
		for _, reply := range drain(send(t, app, got)) {
			send(t, app, reply)
		}
	}
}

// notifyNewFeedback presses N and settles the same review and handoff messages
// the runtime would receive after the watcher's change-detection step.
func notifyNewFeedback(t *testing.T, app *App) {
	t.Helper()
	pump(t, app, send(t, app, press("N")))
}

// lastHandoff reads the final line of the app's handoff log.
func lastHandoff(t *testing.T, app *App) home.Handoff {
	t.Helper()

	data, err := os.ReadFile(app.store.Path(home.HandoffFile))
	require.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var got home.Handoff
	require.NoError(t, json.Unmarshal([]byte(lines[len(lines)-1]), &got))
	return got
}

// pump feeds a command's messages back into the app and follows what those
// produce, but never follows the watcher's own schedule: a test drives the
// beats itself rather than chasing the tick that arranges the next one.
func pump(t *testing.T, app *App, cmd tea.Cmd) {
	t.Helper()

	for _, msg := range drain(cmd) {
		switch msg.(type) {
		case watchTickMsg:
			continue
		case watchSnapshotMsg, watchReviewMsg, watchErrMsg, handoffMsg:
			pump(t, app, send(t, app, msg))
		default:
			send(t, app, msg)
		}
	}
}

// poll drives one beat of the watcher, all the way through to whatever the
// review threads it found led to.
func poll(t *testing.T, app *App) {
	t.Helper()
	pump(t, app, send(t, app, watchTickMsg{seq: app.watchSeq}))
}

func TestOnePollCoversEveryWatchedPullRequestAtOnce(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)

	send(t, app, press("w"))
	send(t, app, press("j"))
	send(t, app, press("w"))
	poll(t, app)

	assert.Equal(t, [][]string{{"PR_42", "PR_7"}}, client.batches(),
		"two pull requests in two repositories, one request")
}

func TestAPullRequestNobodyArmedIsNeverPolled(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)

	send(t, app, press("w"))
	poll(t, app)

	require.Len(t, client.batches(), 1)
	assert.Equal(t, []string{"PR_42"}, client.batches()[0])
}

func TestNothingArmedMeansNoRequestAtAll(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)

	poll(t, app)

	assert.Empty(t, client.batches())
}

func TestNewFeedbackFoundByTheWatcherGoesToAnAgentWithoutBeingAskedTo(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	dispatcher := dispatcherOf(t, app)
	dispatcher.result = handoff.Result{Outcome: home.OutcomeSent, Target: "w2:p1", Kind: "claude"}

	send(t, app, press("w"))
	poll(t, app)

	reqs := dispatcher.requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, 2, reqs[0].NewCount)
	assert.Equal(t, "relloyd/prutil#42", reqs[0].PR.Key().String())
	assert.False(t, reqs[0].AllowProvision, "the automatic watcher must never create a workspace")
}

func TestFeedbackAlreadyHandedOverIsNotSentAgainOnTheNextPoll(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	dispatcher := dispatcherOf(t, app)
	dispatcher.result = handoff.Result{Outcome: home.OutcomeSent, Target: "w2:p1", Kind: "claude"}

	send(t, app, press("w"))
	poll(t, app)
	require.Len(t, dispatcher.requests(), 1)

	// The next look finds the same threads, and a pull request whose comments
	// are never going to be resolved must not cost an agent's time twice.
	advance(app, time.Minute)
	poll(t, app)

	assert.Len(t, dispatcher.requests(), 1)
}

func TestAWatchedPullRequestSaysHowMuchIsWaitingOnIt(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)

	send(t, app, press("w"))
	poll(t, app)

	assert.Contains(t, plain(app.render()), "2 open threads")
}

func TestAPollThatGitHubRefusesIsPushedOutRatherThanRepeated(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	client.watchErr = errors.New("api rate limit exceeded")

	send(t, app, press("w"))
	poll(t, app)

	assert.Contains(t, app.status, "api rate limit exceeded")
	assert.Empty(t, app.engine.Due(app.now()), "the next attempt waits")
	assert.Equal(t, 1, client.watchCalls)
}

func TestDisarmingStopsThePollingAndClearsTheCount(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)

	send(t, app, press("w"))
	poll(t, app)
	require.Contains(t, plain(app.render()), "2 open threads")

	send(t, app, press("w"))
	advance(app, time.Minute)
	poll(t, app)

	assert.Equal(t, 1, client.watchCalls, "a disarmed pull request is not asked about")
	assert.NotContains(t, plain(app.render()), "2 open threads")
}

func TestAPullRequestWithNothingWaitingIsNotHandedToAnybody(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	client.review.Threads = nil
	dispatcher := dispatcherOf(t, app)

	send(t, app, press("w"))
	poll(t, app)

	assert.Empty(t, dispatcher.requests())
}

func TestTheWatcherSaysSoWhenItFindsWorkAndCannotReachHerdr(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	app.hand, app.handErr = nil, errors.New("no herdr server is answering")

	send(t, app, press("w"))
	poll(t, app)

	assert.Equal(t,
		"relloyd/prutil#42 has 2 new review comments, but prutil cannot reach herdr",
		app.status)
}

func TestAPullRequestThatGoesQuietIsMarkedDormantRatherThanPolledForever(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)

	send(t, app, press("w"))
	for range 8 {
		advance(app, time.Minute)
		poll(t, app)
	}

	assert.Contains(t, plain(app.render()), dormantGlyph+" #42")
	before := client.watchCalls
	advance(app, time.Hour)
	poll(t, app)
	assert.Equal(t, before, client.watchCalls, "prutil has stopped asking")
}

func TestARefreshWakesAPullRequestPrutilHadGivenUpOn(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)

	send(t, app, press("w"))
	for range 8 {
		advance(app, time.Minute)
		poll(t, app)
	}
	require.Contains(t, plain(app.render()), dormantGlyph)

	send(t, app, press("r"))
	send(t, app, prsMsg{gen: app.gen, prs: samplePRs()})

	assert.Contains(t, plain(app.render()), watchGlyph+" #42")
	assert.NotEmpty(t, app.engine.Due(app.now()))
}

func TestTheHeaderCountsWhatIsWatchedAndWhenItWillLookAgain(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)

	send(t, app, press("w"))
	poll(t, app)

	header := headerLine(app)
	assert.Contains(t, header, watchGlyph+" 1")
	assert.Contains(t, header, "next ")
}

func TestTheDetailExplainsTheCurrentWatchScheduleAndActivity(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)

	send(t, app, press("w"))

	screen := plain(app.render())
	assert.Contains(t, screen, "WATCH")
	assert.Contains(t, screen, "watching · due now · every 0s",
		"the fast test configuration makes the schedule immediate")
	assert.Contains(t, screen, "waiting for first check")
	assert.Contains(t, screen, "started watching")
}

func TestTheDetailNamesAWatcherOperationWhileItsCommandIsInFlight(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)

	send(t, app, press("w"))
	cmd := send(t, app, watchTickMsg{seq: app.watchSeq})
	require.NotNil(t, cmd)

	assert.Contains(t, plain(app.render()), "checking for changes")
}

func TestTheDetailShowsRecentPersistedHandoffsWithoutReadingDuringRender(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	require.NoError(t, app.store.AppendHandoff(home.Handoff{
		At:          testNow.Add(-time.Minute),
		PR:          "relloyd/prutil#42",
		Outcome:     home.OutcomeSent,
		Kind:        "claude",
		Target:      "w2:p1",
		Provisioned: true,
		Workspace:   "w2",
	}))

	loadHandoffHistory(t, app, samplePRs()[0].Key())

	screen := plain(app.render())
	assert.Contains(t, screen, "WATCH")
	assert.Contains(t, screen, "sent · provisioned · claude w2:p1")
}

func TestTheDetailExplainsUnreadableHandoffHistory(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	require.NoError(t, os.WriteFile(app.store.Path(home.HandoffFile), []byte("{not json}\n"), 0o600))

	loadHandoffHistory(t, app, samplePRs()[0].Key())

	assert.Contains(t, plain(app.render()), "handoff history unavailable")
}

func loadHandoffHistory(t *testing.T, app *App, key model.Key) {
	t.Helper()

	for _, msg := range drain(app.loadHandoffHistory(key, true)) {
		send(t, app, msg)
	}
}
