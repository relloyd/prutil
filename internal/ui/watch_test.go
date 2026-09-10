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

	"github.com/relloyd/prutil/internal/gh"
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

func TestWatcherHandsOffAMarkedSelfAuthoredReviewThread(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	client.review = selfTestReview()
	dispatcher := dispatcherOf(t, app)
	dispatcher.result = handoff.Result{Outcome: home.OutcomeSent, Target: "w2:p1", Kind: "claude"}

	send(t, app, press("w"))
	poll(t, app)

	reqs := dispatcher.requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, 1, reqs[0].UnresolvedCount)
	assert.Equal(t, map[string]string{"self-test": "self-test-comment"}, reqs[0].Threads)
	assert.False(t, reqs[0].AllowProvision, "the automatic watcher must not provision for a test marker")
	assert.Equal(t, map[string]string{"self-test": "self-test-comment"},
		app.state.Get("relloyd/prutil#42").NotifiedThreads)
}

func TestManualDiscoveryHandsOffAMarkedSelfAuthoredReviewThread(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	client.review = selfTestReview()
	dispatcher := dispatcherOf(t, app)
	dispatcher.result = handoff.Result{Outcome: home.OutcomeSent, Target: "w2:p1", Kind: "claude"}

	notifyNewFeedback(t, app)

	reqs := dispatcher.requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, 1, reqs[0].NewCount)
	assert.False(t, reqs[0].AllowProvision, "N must use automatic handoff semantics for a test marker")
}

func TestManualDiscoveryHandsOffAMarkedLatestReplyFromTheViewer(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	client.review = latestReplySelfTestReview()
	dispatcher := dispatcherOf(t, app)
	dispatcher.result = handoff.Result{Outcome: home.OutcomeSent, Target: "w2:p1", Kind: "claude"}

	notifyNewFeedback(t, app)

	reqs := dispatcher.requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, 1, reqs[0].UnresolvedCount)
	assert.Equal(t, 1, reqs[0].NewCount)
	assert.Equal(t, map[string]string{"copilot-thread": "viewer-test-reply"}, reqs[0].Threads)
	assert.False(t, reqs[0].AllowProvision, "N must retain automatic no-provisioning semantics")
	assert.Equal(t, map[string]string{"copilot-thread": "viewer-test-reply"},
		app.state.Get("relloyd/prutil#42").NotifiedThreads)

	notifyNewFeedback(t, app)
	assert.Len(t, dispatcher.requests(), 1, "an unchanged marked reply is not handed over twice")
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

	got := model.Feedback(threads, "relloyd", model.DefaultSelfTestMarker)
	require.Len(t, got, 2)
	assert.Equal(t, "T1", got[0].ID)
	assert.Equal(t, "T2", got[1].ID)
}

// selfTestReview is a code-line thread a viewer deliberately created to
// exercise watcher delivery without requiring another reviewer.
func selfTestReview() gh.Review {
	return gh.Review{
		Viewer: "relloyd",
		Threads: []model.ReviewThread{{
			ID:       "self-test",
			Opener:   "relloyd",
			Body:     "Exercise watcher delivery.\n" + model.DefaultSelfTestMarker,
			LatestBy: "relloyd",
			LatestID: "self-test-comment",
		}},
	}
}

func latestReplySelfTestReview() gh.Review {
	return gh.Review{
		Viewer: "relloyd",
		Threads: []model.ReviewThread{{
			ID:         "copilot-thread",
			Opener:     "copilot-pull-request-reviewer",
			Body:       "Please handle this permission.",
			LatestBy:   "relloyd",
			LatestID:   "viewer-test-reply",
			LatestBody: "Acknowledged for watcher testing.\n\n" + model.DefaultSelfTestMarker,
		}},
	}
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

func TestTheWatchSectionCanBeSelectedAndDrilledInto(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)

	send(t, app, press("w"))
	send(t, app, press("l"))
	require.Equal(t, paneDetail, app.focus)
	require.Equal(t, detailChecks, app.section)

	send(t, app, press("k"))
	assert.Equal(t, detailWatch, app.section)
	assert.Contains(t, plain(app.render()), "▌ WATCH")

	send(t, app, press("l"))
	assert.Equal(t, detailWatchPage, app.page)
	screen := plain(app.render())
	assert.Contains(t, screen, "cadence:")
	assert.Contains(t, screen, "ACTIVITY")
	assert.Contains(t, screen, "HANDOFFS")
	assert.Contains(t, screen, "started watching")

	send(t, app, press("h"))
	assert.Equal(t, detailOverview, app.page)
	assert.Equal(t, paneDetail, app.focus)
	send(t, app, press("h"))
	assert.Equal(t, paneList, app.focus)
}

func TestExpandedWatchViewShowsPersistedHandoffMetadata(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	require.NoError(t, app.store.AppendHandoff(home.Handoff{
		At:          testNow.Add(-time.Minute),
		PR:          "relloyd/prutil#42",
		Outcome:     home.OutcomeSent,
		Kind:        "claude",
		Target:      "w2:p1",
		Workspace:   "w2",
		Tab:         "w2:t1",
		Provisioned: true,
	}))
	loadHandoffHistory(t, app, samplePRs()[0].Key())

	send(t, app, press("w"))
	send(t, app, press("l"))
	send(t, app, press("k"))
	send(t, app, press("l"))

	screen := plain(app.render())
	assert.Contains(t, screen, "workspace: w2")
	assert.Contains(t, screen, "tab: w2:t1")
}

func TestExpandedWatchViewScrollsThroughExistingActivity(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 14)
	key := samplePRs()[0].Key()
	for range watchActivityLimit {
		app.recordWatchActivity(key, "additional watcher activity")
	}

	send(t, app, press("w"))
	send(t, app, press("l"))
	send(t, app, press("k"))
	send(t, app, press("l"))
	require.Greater(t, app.watchLineCount(), app.watchWindow())

	send(t, app, press("G"))
	assert.Greater(t, app.watchOffset, 0)
	assert.Contains(t, plain(app.render()), " of ")

	send(t, app, press("g"))
	assert.Zero(t, app.watchOffset)
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
	// A log that cannot be read at all is worth saying so about. A single line
	// that will not parse is not: the log is appended a line at a time, so the
	// damage is a write cut short, and the handoffs either side of it are
	// still worth showing.
	app, _, _ := newTestApp(t, 120, 40)
	require.NoError(t, os.MkdirAll(app.store.Path(home.HandoffFile), 0o700))

	loadHandoffHistory(t, app, samplePRs()[0].Key())

	assert.Contains(t, plain(app.render()), "handoff history unavailable")
}

func TestTheDetailStillShowsHandoffsEitherSideOfADamagedLine(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	key := samplePRs()[0].Key()
	require.NoError(t, app.store.AppendHandoff(home.Handoff{
		At: testNow.Add(-time.Hour), PR: key.String(), Outcome: home.OutcomeSent, Kind: "claude",
	}))
	file, err := os.OpenFile(app.store.Path(home.HandoffFile), os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = file.WriteString(`{"pr":"` + key.String() + `","outc`)
	require.NoError(t, err)
	require.NoError(t, file.Close())

	loadHandoffHistory(t, app, key)

	screen := plain(app.render())
	assert.NotContains(t, screen, "handoff history unavailable")
	assert.Contains(t, screen, "sent · claude")
}

func loadHandoffHistory(t *testing.T, app *App, key model.Key) {
	t.Helper()

	for _, msg := range drain(app.loadHandoffHistory(key, true)) {
		send(t, app, msg)
	}
}

func TestAPollThatGoesUnansweredIsPushedOutRatherThanRepeatedAtOnce(t *testing.T) {
	// A pull request GitHub answers with a null node produces no reading, so
	// the engine has nothing to reschedule it from. Left where it was, its next
	// poll stays due in the past, the schedule computes a zero delay, and the
	// request repeats as fast as the round trip allows.
	app, _, _ := newTestApp(t, 120, 40)
	send(t, app, press("w"))

	pr, ok := app.selectedPR()
	require.True(t, ok)
	key := pr.Key()
	require.Equal(t, 1, app.engine.Watching())

	send(t, app, watchSnapshotMsg{keys: []model.Key{key}, snaps: nil})

	status, armed := app.engine.Status(key)
	require.True(t, armed)
	assert.True(t, status.NextDue.After(app.now()),
		"the next poll is in the future, not due again immediately")
	assert.Empty(t, app.engine.Due(app.now()), "nothing is due the moment the reply lands")
	assert.Contains(t, activityText(app, key), "GitHub said nothing about it")
}

func TestAPollIsStillAppliedToThePullRequestsThatWereAnswered(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	send(t, app, press("w"))

	pr, ok := app.selectedPR()
	require.True(t, ok)
	key := pr.Key()

	send(t, app, watchSnapshotMsg{
		keys:  []model.Key{key},
		snaps: []model.Snapshot{{Key: key, NodeID: pr.NodeID, HeadOID: "abc", UpdatedAt: testNow}},
	})

	assert.Contains(t, activityText(app, key), "changes found",
		"a first reading is a change, so the precise query follows")
	assert.NotContains(t, activityText(app, key), "GitHub said nothing about it")
}

// activityText joins everything the watcher has recorded for one pull request.
func activityText(app *App, key model.Key) string {
	var out []string
	for _, event := range app.runtimeOf(key).activity {
		out = append(out, event.text)
	}
	return strings.Join(out, " | ")
}

func TestTheWatchSectionStaysAwayWhileItsHistoryIsStillLoading(t *testing.T) {
	// Selecting a pull request starts a durable-history read. Counting that
	// read as content opened the section on every selection and shut it again
	// a moment later for the ordinary pull request nothing has been handed off
	// for, moving the checks beneath it twice for nothing.
	app, _, _ := newTestApp(t, 120, 40)
	pr, ok := app.selectedPR()
	require.True(t, ok)

	require.True(t, app.runtimeOf(pr.Key()).history.loading, "the history read is in flight")
	assert.False(t, app.hasWatchSection(pr), "a read in flight is not something to show")
	assert.NotContains(t, plain(app.render()), "WATCH")

	send(t, app, handoffHistoryMsg{key: pr.Key(), generation: app.runtimeOf(pr.Key()).history.generation})

	assert.False(t, app.hasWatchSection(pr), "an empty history leaves nothing to show either")
	assert.NotContains(t, plain(app.render()), "WATCH", "the section never appeared, so it cannot vanish")
}

func TestTheWatchSectionStaysOnceItHasSomethingToSay(t *testing.T) {
	// Disarming is not the same as having nothing to show: what the watcher
	// did this session is still worth reading, so the section stays and the
	// selection on it stays valid.
	app, _, _ := newTestApp(t, 120, 40)
	send(t, app, press("w"))

	pr, ok := app.selectedPR()
	require.True(t, ok)
	send(t, app, press("l"))
	send(t, app, press("k"))
	require.Equal(t, detailWatch, app.section)

	send(t, app, press("w"))

	assert.True(t, app.hasWatchSection(pr), "the activity from this session is still worth showing")
	assert.Equal(t, detailWatch, app.section, "the selection is still on something real")
	assert.Contains(t, plain(app.render()), "WATCH")
}

func TestASelectionLeftOnAWatchSectionThatIsGoneFallsBackToTheChecks(t *testing.T) {
	// The section is built from state that can empty, so the guard is written
	// against the state rather than against any one key sequence: a selection
	// pointing at a heading that is not drawn highlights nothing, and drilling
	// into it opens a page with no lines.
	app, _, _ := newTestApp(t, 120, 40)
	pr, ok := app.selectedPR()
	require.True(t, ok)
	require.False(t, app.hasWatchSection(pr), "nothing armed and nothing recorded")

	app.focus = paneDetail
	app.section = detailWatch

	app.clampScroll()
	assert.Equal(t, detailChecks, app.section, "the stale selection falls back")

	app.section = detailWatch
	send(t, app, press("l"))
	assert.Equal(t, detailOverview, app.page, "there is no page to drill into")
	assert.NotContains(t, plain(app.render()), "nothing to show for WATCH")
}

func TestAWatchPageLeftOpenOnAnEmptySectionCloses(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	pr, ok := app.selectedPR()
	require.True(t, ok)
	require.False(t, app.hasWatchSection(pr))

	app.focus = paneDetail
	app.section, app.page = detailWatch, detailWatchPage

	app.clampScroll()

	assert.Equal(t, detailOverview, app.page)
	assert.Equal(t, detailChecks, app.section)
	assert.Zero(t, app.watchOffset)
}

func TestTheCompactWatchSectionSpendsItsWidthOnFactsAndThePageOnLabels(t *testing.T) {
	// Both renderers read one set of facts and phrase them for the room they
	// have. Beside the checks the colour says which fact it is; on a page of
	// its own there is room to name it.
	app, _, _ := newTestApp(t, 120, 40)
	send(t, app, press("w"))
	pr, ok := app.selectedPR()
	require.True(t, ok)
	setFeedback(app, pr.Key(), 3)

	compact := plain(app.render())
	assert.Contains(t, compact, "3 open threads")
	assert.NotContains(t, compact, "feedback: 3 open threads")

	send(t, app, press("l"))
	send(t, app, press("k"))
	send(t, app, press("l"))
	require.Equal(t, detailWatchPage, app.page)

	page := plain(app.render())
	assert.Contains(t, page, "feedback: 3 open threads")
	assert.Contains(t, page, "cadence: every")
	assert.Contains(t, page, "next check: ")
}

func TestTheCompactWatchSectionNamesTheWorkInFlightWithoutLabellingIt(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	send(t, app, press("w"))
	pr, ok := app.selectedPR()
	require.True(t, ok)
	app.setWatchOperation(pr.Key(), "handing feedback to an agent")

	compact := plain(app.render())
	assert.Contains(t, compact, "handing feedback to an agent")
	assert.NotContains(t, compact, "operation: handing feedback")

	send(t, app, press("l"))
	send(t, app, press("k"))
	send(t, app, press("l"))
	assert.Contains(t, plain(app.render()), "operation: handing feedback to an agent")
}

func TestTheExpandedWatchPageIsMeasuredByCountingRatherThanRendering(t *testing.T) {
	// The scroll arithmetic asks how long the page is on every key press, so
	// the answer must not cost a screenful of styled and truncated text.
	app, _, _ := newTestApp(t, 120, 60)
	send(t, app, press("w"))
	pr, ok := app.selectedPR()
	require.True(t, ok)
	app.recordWatchActivity(pr.Key(), "started a change check")
	app.recordWatchActivity(pr.Key(), "changes found")

	send(t, app, press("l"))
	send(t, app, press("k"))
	send(t, app, press("l"))
	require.Equal(t, detailWatchPage, app.page)

	_, width := app.paneWidths()
	rendered := app.renderWatchPage(pr, width, app.bodyHeight())
	require.Less(t, len(rendered), app.bodyHeight(), "the whole page fits, so nothing is scrolled away")
	assert.Equal(t, app.watchLineCount(), len(rendered),
		"counted rows and drawn lines are the same page")
}

func TestEverySectionHeadingLinesUpWithTheOthers(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 60)
	send(t, app, press("w"))
	pr, ok := app.selectedPR()
	require.True(t, ok)
	app.recordWatchActivity(pr.Key(), "started a change check")

	send(t, app, press("l"))
	send(t, app, press("k"))
	send(t, app, press("l"))

	_, width := app.paneWidths()
	for _, heading := range []string{"WATCH", "ACTIVITY", "HANDOFFS"} {
		found := false
		for _, line := range app.renderWatchPage(pr, width, app.bodyHeight()) {
			if strings.TrimSpace(plain(line)) == heading {
				assert.Equal(t, "  "+heading, plain(line), "%s carries the same indent as the rest", heading)
				found = true
			}
		}
		assert.Truef(t, found, "%s is on the page", heading)
	}
}

func TestOnePullRequestsWatcherStateLivesInOneEntry(t *testing.T) {
	// Seven maps written from six places and cleaned up from four is how a
	// cleanup comes to reach five of them. One entry is cleared in one place.
	app, _, _ := newTestApp(t, 120, 40)
	send(t, app, press("w"))
	pr, ok := app.selectedPR()
	require.True(t, ok)
	key := pr.Key()

	// Everything on this pull request has already been handed over, so the
	// read finishes without starting anything else.
	app.state.RecordHandoff(key.String(), model.Digest(sampleThreads().Feedback(model.DefaultSelfTestMarker)), testNow)

	send(t, app, watchSnapshotMsg{
		keys:  []model.Key{key},
		snaps: []model.Snapshot{{Key: key, NodeID: pr.NodeID, HeadOID: "abc", UpdatedAt: testNow}},
	})
	require.True(t, app.runtimeOf(key).reviewing, "the precise read is in flight")
	require.NotEmpty(t, app.runtimeOf(key).operation, "and the pane says so")

	send(t, app, watchReviewMsg{key: key, review: sampleThreads()})

	got := app.runtimeOf(key)
	assert.False(t, got.reviewing, "the read is done")
	assert.False(t, got.handing, "and nothing new to hand over")
	assert.Empty(t, got.operation, "so nothing is left saying otherwise")
	assert.True(t, got.hasFeedback, "what it found is recorded")
	assert.NotEmpty(t, got.activity, "along with why")
}

func TestClearingAnOperationThatWasNeverSetRemembersNothing(t *testing.T) {
	// Entries exist for pull requests something has happened to, the durable
	// history read that follows a selection included. Clearing an operation
	// must not add to that: it runs for every pull request a poll covered.
	app, _, _ := newTestApp(t, 120, 40)
	before := len(app.runtime)

	for _, pr := range app.cur().prs {
		app.setWatchOperation(pr.Key(), "")
	}

	assert.Len(t, app.runtime, before, "clearing what was never set conjures nothing")
	for _, pr := range app.cur().prs {
		assert.Empty(t, app.runtimeOf(pr.Key()).operation)
	}
}
