package ui

import (
	"errors"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/relloyd/prutil/internal/model"
)

func TestQuitKeys(t *testing.T) {
	for _, keystroke := range []string{"q", "ctrl+c"} {
		t.Run(keystroke, func(t *testing.T) {
			app, _, _ := newTestApp(t, 120, 40)

			cmd := send(t, app, press(keystroke))
			require.NotNil(t, cmd, "%s must produce a command", keystroke)
			assert.IsType(t, tea.QuitMsg{}, cmd())
		})
	}
}

func TestListNavigation(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	require.Len(t, app.cur().prs, 3)

	send(t, app, press("j"))
	assert.Equal(t, 1, app.cur().cursor)

	send(t, app, press("down"))
	assert.Equal(t, 2, app.cur().cursor)

	send(t, app, press("j"))
	assert.Equal(t, 2, app.cur().cursor, "the cursor stops at the last pull request")

	send(t, app, press("k"))
	assert.Equal(t, 1, app.cur().cursor)

	send(t, app, press("g"))
	assert.Equal(t, 0, app.cur().cursor)

	send(t, app, press("k"))
	assert.Equal(t, 0, app.cur().cursor, "the cursor stops at the first pull request")

	send(t, app, press("G"))
	assert.Equal(t, 2, app.cur().cursor)
}

func TestFocusMovesBetweenPanes(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	require.Equal(t, paneList, app.focus)

	send(t, app, press("l"))
	assert.Equal(t, paneDetail, app.focus)
	assert.Equal(t, 0, app.detailCursor)

	send(t, app, press("j"))
	assert.Equal(t, 1, app.detailCursor)
	assert.Equal(t, 0, app.cur().cursor, "moving in the detail pane leaves the list alone")

	send(t, app, press("G"))
	assert.Equal(t, 2, app.detailCursor, "the detail cursor stops at the last check")

	send(t, app, press("h"))
	assert.Equal(t, paneList, app.focus)

	send(t, app, press("right"))
	assert.Equal(t, paneDetail, app.focus)
	send(t, app, press("esc"))
	assert.Equal(t, paneList, app.focus)
}

func TestFocusDoesNotEnterAnEmptyList(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	send(t, app, prsMsg{gen: app.gen, prs: nil})

	send(t, app, press("l"))
	assert.Equal(t, paneList, app.focus, "there is nothing to show in the detail pane")
}

func TestMovingTheListResetsTheDetailCursor(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	send(t, app, press("l"))
	send(t, app, press("j"))
	require.Equal(t, 1, app.detailCursor)

	send(t, app, press("h"))
	send(t, app, press("j"))
	assert.Equal(t, 0, app.detailCursor, "a different pull request starts at its first check")
}

func TestEnterOpensTheSelectedPullRequest(t *testing.T) {
	app, _, opener := newTestApp(t, 120, 40)

	send(t, app, press("j"))
	cmd := send(t, app, press("enter"))
	require.NotNil(t, cmd)

	msg := cmd()
	assert.Equal(t, []string{"https://github.com/relloyd/other/pull/7"}, opener.opened())
	assert.Equal(t, statusMsg("opened relloyd/other#7"), msg)
}

func TestEnterOpensTheSelectedCheck(t *testing.T) {
	app, _, opener := newTestApp(t, 120, 40)

	send(t, app, press("l"))
	send(t, app, press("j"))
	cmd := send(t, app, press("enter"))
	require.NotNil(t, cmd)
	cmd()

	assert.Equal(t, []string{"https://github.com/relloyd/prutil/actions/runs/1/job/2"}, opener.opened())
}

func TestEnterFallsBackToThePullRequestWhenACheckHasNoURL(t *testing.T) {
	app, _, opener := newTestApp(t, 120, 40)
	key := model.Key{Repo: "relloyd/prutil", Number: 42}
	send(t, app, checksMsg{gen: app.gen, key: key, checks: []model.Check{{Name: "unlinked"}}})

	send(t, app, press("l"))
	cmd := send(t, app, press("enter"))
	require.NotNil(t, cmd)
	cmd()

	assert.Equal(t, []string{"https://github.com/relloyd/prutil/pull/42"}, opener.opened())
}

func TestEnterReportsBrowserFailures(t *testing.T) {
	app, _, opener := newTestApp(t, 120, 40)
	opener.err = errors.New("xdg-open missing")

	cmd := send(t, app, press("enter"))
	require.NotNil(t, cmd)

	msg, ok := cmd().(statusMsg)
	require.True(t, ok)
	assert.Contains(t, string(msg), "xdg-open missing")
	assert.Empty(t, opener.opened())
}

func TestRefreshStartsANewGenerationAndDropsStaleReplies(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	before := app.gen
	key := model.Key{Repo: "relloyd/prutil", Number: 42}
	require.NotEmpty(t, app.checks[key].checks)

	send(t, app, press("r"))
	assert.Equal(t, before+1, app.gen)
	assert.True(t, app.cur().loading)
	assert.Empty(t, app.checks, "the check cache is dropped on refresh")

	// A reply from the previous generation must not be applied.
	send(t, app, checksMsg{gen: before, key: key, checks: sampleChecks()[key]})
	assert.Empty(t, app.checks[key].checks)

	send(t, app, prsMsg{gen: before, prs: nil})
	assert.Len(t, app.cur().prs, 3, "a stale list does not wipe the current one")
	assert.True(t, app.cur().loading)

	// The current generation is applied.
	send(t, app, prsMsg{gen: app.gen, prs: samplePRs()})
	assert.False(t, app.cur().loading)
	assert.Zero(t, client.listCalls, "the refresh command is returned to Bubble Tea, not run inline")
}

func TestRefreshKeepsTheCursorOnTheSamePullRequest(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	send(t, app, press("j"))
	send(t, app, press("j"))
	require.Equal(t, "relloyd/third", app.cur().prs[app.cur().cursor].Repo)

	reordered := []model.PullRequest{samplePRs()[2], samplePRs()[0], samplePRs()[1]}
	send(t, app, prsMsg{gen: app.gen, prs: reordered})

	assert.Equal(t, 0, app.cur().cursor)
	assert.Equal(t, "relloyd/third", app.cur().prs[app.cur().cursor].Repo)
}

func TestRefreshResetsTheCursorWhenThePullRequestIsGone(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	send(t, app, press("G"))
	require.Equal(t, 2, app.cur().cursor)

	send(t, app, prsMsg{gen: app.gen, prs: samplePRs()[:1]})
	assert.Equal(t, 0, app.cur().cursor)
}

func TestSelectionFetchesChecksOnlyForTheCurrentSelection(t *testing.T) {
	client := newFakeClient(samplePRs(), sampleChecks())
	opener := &fakeOpener{}
	app := New(Config{Client: client, Opener: opener, Now: func() time.Time { return testNow }})
	send(t, app, tea.WindowSizeMsg{Width: 120, Height: 40})
	send(t, app, prsMsg{gen: app.gen, prs: samplePRs()})

	second := samplePRs()[1].Key()

	// A debounced selection message for a pull request that is no longer under
	// the cursor is ignored.
	app.checks = map[model.Key]checkState{}
	cmd := send(t, app, selectionMsg{gen: app.gen, key: second})
	assert.Nil(t, cmd, "the selection moved on, so nothing is fetched")

	send(t, app, press("j"))
	cmd = send(t, app, selectionMsg{gen: app.gen, key: second})
	require.NotNil(t, cmd, "the selection still matches, so the checks are fetched")

	assert.Contains(t, drain(cmd), checksMsg{gen: app.gen, key: second})
	assert.Equal(t, 1, client.callsFor(second))
}

func TestMovingTheCursorSchedulesADebouncedFetch(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)

	cmd := send(t, app, press("j"))
	require.NotNil(t, cmd, "moving schedules a check fetch")

	msg := cmd()
	selection, ok := msg.(selectionMsg)
	require.True(t, ok, "expected a debounced selection message, got %T", msg)
	assert.Equal(t, samplePRs()[1].Key(), selection.key)
	assert.Equal(t, app.gen, selection.gen)
}

func TestChecksAreFetchedOnce(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	key := samplePRs()[0].Key()

	assert.Nil(t, app.ensureChecks(key), "cached checks are not fetched again")
	assert.Equal(t, 0, client.callsFor(key))

	app.checks = map[model.Key]checkState{}
	require.NotNil(t, app.ensureChecks(key))
	assert.Nil(t, app.ensureChecks(key), "an in-flight fetch is not repeated")
}

func TestListErrorIsShownAndCleared(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)

	send(t, app, errMsg{gen: app.gen, err: errors.New("HTTP 401: Bad credentials")})
	assert.False(t, app.cur().loading)
	require.Error(t, app.cur().err)
	assert.Contains(t, app.render(), "HTTP 401")

	send(t, app, prsMsg{gen: app.gen, prs: samplePRs()})
	assert.NoError(t, app.cur().err, "a successful load clears the error")
}

func TestCheckErrorIsRecordedAgainstThePullRequest(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	key := samplePRs()[0].Key()

	send(t, app, checksErrMsg{gen: app.gen, key: key, err: errors.New("rate limited")})
	assert.Error(t, app.checks[key].err)
	assert.True(t, app.checks[key].loaded)
	assert.Contains(t, app.render(), "checks unavailable")
}

func TestStatusMessagesExpire(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)

	send(t, app, statusMsg("opened relloyd/prutil#42"))
	assert.Contains(t, app.render(), "opened relloyd/prutil#42")

	send(t, app, clearStatusMsg{})
	assert.NotContains(t, app.render(), "opened relloyd/prutil#42")
}

func TestTabLoadsTheClosedViewOnceAndSwitchesBack(t *testing.T) {
	app, client, _ := newTestApp(t, 120, 40)
	require.Equal(t, viewOpen, app.active)
	require.Zero(t, client.closedCallCount())

	// The closed view is not fetched until it is first shown.
	cmd := send(t, app, press("tab"))
	require.Equal(t, viewClosed, app.active)
	require.NotNil(t, cmd)
	assert.True(t, app.cur().loading)
	assert.Empty(t, app.cur().prs, "the closed list is empty until its reply lands")

	for _, msg := range drain(cmd) {
		send(t, app, msg)
	}
	assert.Equal(t, 1, client.closedCallCount())
	require.Len(t, app.cur().prs, 4)
	assert.Equal(t, "relloyd/prutil", app.cur().prs[0].Repo)

	// Switching away and back neither refetches nor loses the open list.
	send(t, app, press("tab"))
	require.Equal(t, viewOpen, app.active)
	assert.Len(t, app.cur().prs, 3)

	send(t, app, press("tab"))
	assert.Equal(t, viewClosed, app.active)
	assert.Equal(t, 1, client.closedCallCount(), "an already loaded view is not fetched again")
}

func TestEachViewKeepsItsOwnCursor(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	send(t, app, press("j"))
	send(t, app, press("j"))
	require.Equal(t, 2, app.cur().cursor)

	send(t, app, press("tab"))
	send(t, app, prsMsg{gen: app.gen, view: viewClosed, prs: sampleClosedPRs()})
	require.Equal(t, 0, app.cur().cursor)
	send(t, app, press("j"))
	assert.Equal(t, 1, app.cur().cursor)

	send(t, app, press("tab"))
	assert.Equal(t, 2, app.cur().cursor, "the open view kept its place")
}

func TestClosedViewRendersOutcomeBadgesAndCloseAge(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	send(t, app, press("tab"))
	send(t, app, prsMsg{gen: app.gen, view: viewClosed, prs: sampleClosedPRs()})

	screen := ansi.Strip(app.render())
	assert.Contains(t, screen, "MERGED")
	assert.Contains(t, screen, "CLOSED")
	assert.Contains(t, screen, "4 closed PRs")
	assert.Contains(t, screen, "1d ago", "the closed view dates rows by when they closed")
	assert.NotContains(t, screen, "CONFLICT", "a closed pull request cannot conflict any more")
}

func TestRefreshReloadsTheVisibleViewAndInvalidatesTheOther(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	send(t, app, press("tab"))
	send(t, app, prsMsg{gen: app.gen, view: viewClosed, prs: sampleClosedPRs()})
	require.True(t, app.views[viewClosed].loaded)

	// Refreshing the closed view leaves the open list on screen but marks it
	// for a refetch, so the reader never sees pre-refresh data.
	send(t, app, press("r"))
	assert.True(t, app.views[viewClosed].loading)
	assert.False(t, app.views[viewOpen].loaded)
	assert.Len(t, app.views[viewOpen].prs, 3, "the stale list stays visible until its replacement lands")
}

func TestABackgroundViewErrorDoesNotDisturbTheVisibleOne(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)

	send(t, app, errMsg{gen: app.gen, view: viewClosed, err: errors.New("HTTP 502")})
	assert.NoError(t, app.cur().err, "an error in a hidden view is not shown")
	assert.NotContains(t, app.render(), "HTTP 502")

	send(t, app, press("tab"))
	assert.Contains(t, app.render(), "HTTP 502")
}

func TestHelpToggles(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	require.False(t, app.showHelp)

	send(t, app, press("?"))
	assert.True(t, app.showHelp)
	assert.True(t, app.help.ShowAll)

	send(t, app, press("?"))
	assert.False(t, app.showHelp)
}

func TestBackgroundColourRebuildsTheStyles(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)

	send(t, app, tea.BackgroundColorMsg{Color: lightBackground{}})
	assert.NotNil(t, app.styles.Accent)
	assert.NotEmpty(t, app.render(), "the app still renders after a theme change")
}

func TestPrefetchWarmsTheTopOfTheList(t *testing.T) {
	client := newFakeClient(samplePRs(), sampleChecks())
	app := New(Config{Client: client, Opener: &fakeOpener{}, Now: func() time.Time { return testNow }})
	send(t, app, tea.WindowSizeMsg{Width: 120, Height: 40})

	cmd := send(t, app, prsMsg{gen: app.gen, prs: samplePRs()})
	require.NotNil(t, cmd)
	drain(cmd)

	for _, pr := range samplePRs() {
		assert.Equal(t, 1, client.callsFor(pr.Key()), "%s should be prefetched exactly once", pr.Key())
	}
}

func TestBusyTracksInFlightWork(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	assert.False(t, app.busy())

	app.checks[samplePRs()[0].Key()] = checkState{loading: true}
	assert.True(t, app.busy())
}

func TestClampOffset(t *testing.T) {
	cases := []struct {
		name                          string
		offset, cursor, window, count int
		want                          int
	}{
		{"empty list", 0, 0, 5, 0, 0},
		{"cursor above the window", 4, 2, 3, 10, 2},
		{"cursor below the window", 0, 7, 3, 10, 5},
		{"cursor inside the window", 3, 4, 3, 10, 3},
		{"offset past the end", 9, 1, 3, 10, 1},
		{"list shorter than the window", 0, 0, 20, 3, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, clampOffset(c.offset, c.cursor, c.window, c.count))
		})
	}
}

func TestRefreshKeepsThePlaceInABackgroundView(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	send(t, app, press("tab"))
	send(t, app, prsMsg{gen: app.gen, view: viewClosed, prs: sampleClosedPRs()})
	send(t, app, press("j"))
	send(t, app, press("j"))
	require.Equal(t, 2, app.cur().cursor)

	// Refresh from the open view, which invalidates the closed one.
	send(t, app, press("tab"))
	require.Equal(t, viewOpen, app.active)
	send(t, app, press("r"))
	require.False(t, app.views[viewClosed].loaded)

	assert.Equal(t, 2, app.views[viewClosed].cursor,
		"an invalidated view keeps the cursor, so its reload can restore the selection")
	assert.Len(t, app.views[viewClosed].prs, 4, "and keeps its list to show while reloading")
}

func TestClosedViewReportsUnreachableRepositories(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	send(t, app, press("tab"))

	// A large organisation can make GitHub refuse some of the batched
	// searches. The rows that did arrive are still worth showing, so the app
	// renders them and says the list is short rather than showing an error.
	send(t, app, prsMsg{gen: app.gen, view: viewClosed, prs: sampleClosedPRs(), unavailable: 3})

	screen := ansi.Strip(app.render())
	assert.Contains(t, screen, "4 closed PRs")
	assert.Contains(t, screen, "3 repos unreachable")
	assert.NoError(t, app.cur().err, "an incomplete list is not an error")
}

func TestCompleteClosedViewSaysNothingAboutReachability(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	send(t, app, press("tab"))
	send(t, app, prsMsg{gen: app.gen, view: viewClosed, prs: sampleClosedPRs()})

	assert.NotContains(t, ansi.Strip(app.render()), "unreachable")
}
