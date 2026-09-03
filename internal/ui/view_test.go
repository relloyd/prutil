package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/relloyd/prutil/internal/model"
)

// plain strips styling so that assertions read like the screen looks.
func plain(s string) string { return ansi.Strip(s) }

// lines returns the rendered screen with styling removed.
func lines(app *App) []string {
	return strings.Split(plain(app.render()), "\n")
}

func TestRenderFitsEveryTerminalSize(t *testing.T) {
	sizes := []struct{ width, height int }{
		{40, 12}, {60, 20}, {79, 24}, {80, 24}, {100, 30}, {200, 60}, {120, 8},
	}
	for _, size := range sizes {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			app, _, _ := newTestApp(t, size.width, size.height)

			for _, focus := range []pane{paneList, paneDetail} {
				app.focus = focus
				rendered := lines(app)

				assert.LessOrEqual(t, len(rendered), size.height, "the screen must not overflow vertically")
				for i, line := range rendered {
					assert.LessOrEqual(t, ansi.StringWidth(line), size.width,
						"line %d overflows the terminal: %q", i, line)
				}
			}
		})
	}
}

func TestWideLayoutShowsBothPanes(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	screen := plain(app.render())

	assert.Contains(t, screen, "relloyd/prutil", "the list shows the repository")
	assert.Contains(t, screen, "#42")
	assert.Contains(t, screen, "feat/uploader-retry")
	assert.Contains(t, screen, "→")
	assert.Contains(t, screen, "2d old")
	assert.Contains(t, screen, "upd 2h")
	assert.Contains(t, screen, "APPROVED")
	assert.Contains(t, screen, "DRAFT")
	assert.Contains(t, screen, "CONFLICT")
	assert.Contains(t, screen, "+120 −30 · 7 files")
	assert.Contains(t, screen, "✓1 ✗1 ●1", "the check tally is visible without opening the detail pane")

	assert.Contains(t, screen, "CHECKS (3)", "the detail pane is beside the list")
	assert.Contains(t, screen, "lint")
	assert.Contains(t, screen, "opened 2d ago")
	assert.Contains(t, screen, "4 comments")
}

func TestNarrowLayoutShowsOnePaneAtATime(t *testing.T) {
	app, _, _ := newTestApp(t, 60, 24)
	require.True(t, app.narrow())

	list := plain(app.render())
	assert.Contains(t, list, "pull requests", "the header says which pane is showing")
	assert.Contains(t, list, "relloyd/prutil")
	assert.NotContains(t, list, "CHECKS (3)", "the detail pane is hidden while the list has focus")

	send(t, app, press("l"))
	detail := plain(app.render())
	assert.Contains(t, detail, "checks")
	assert.Contains(t, detail, "CHECKS (3)")
	assert.NotContains(t, detail, "relloyd/other", "the list is hidden while the detail pane has focus")

	send(t, app, press("h"))
	assert.Contains(t, plain(app.render()), "relloyd/other")
}

func TestResizeSwitchesBetweenLayouts(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	require.False(t, app.narrow())
	assert.Contains(t, plain(app.render()), "CHECKS (3)")

	send(t, app, tea.WindowSizeMsg{Width: 50, Height: 24})
	assert.True(t, app.narrow())
	assert.NotContains(t, plain(app.render()), "CHECKS (3)")

	send(t, app, tea.WindowSizeMsg{Width: 120, Height: 40})
	assert.False(t, app.narrow())
	assert.Contains(t, plain(app.render()), "CHECKS (3)")
}

func TestLongTitlesWrapRatherThanOverflow(t *testing.T) {
	app, _, _ := newTestApp(t, 60, 30)
	screen := lines(app)

	var wrapped int
	for _, line := range screen {
		if strings.Contains(line, "Add a retry") || strings.Contains(line, "uploader") {
			wrapped++
		}
	}
	assert.GreaterOrEqual(t, wrapped, 2, "a long title occupies more than one line")
}

func TestListScrollsToKeepTheCursorVisible(t *testing.T) {
	app, _, _ := newTestApp(t, 100, 14)
	require.Less(t, app.bodyHeight()/rowHeight, len(app.prs), "the test needs a list taller than the pane")

	assert.Contains(t, plain(app.render()), "#42")

	send(t, app, press("G"))
	screen := plain(app.render())
	assert.Contains(t, screen, "#9", "the last pull request scrolls into view")
	assert.Contains(t, screen, "of 3", "the pane reports the window it is showing")
}

func TestDetailPaneReportsLoadingAndEmptyStates(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	key := samplePRs()[0].Key()

	app.checks[key] = checkState{loading: true}
	assert.Contains(t, plain(app.render()), "loading checks…")

	send(t, app, checksMsg{gen: app.gen, key: key, checks: nil})
	assert.Contains(t, plain(app.render()), "no checks on the head commit")
}

func TestSelectedCheckIsMarkedOnlyWhenTheDetailPaneHasFocus(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)

	assert.NotContains(t, checkLine(t, app, "lint"), "▌")

	send(t, app, press("l"))
	send(t, app, press("j"))
	assert.Contains(t, checkLine(t, app, "lint"), "▌", "the focused check carries the selection bar")
}

func TestEmptyListIsExplained(t *testing.T) {
	app, _, _ := newTestApp(t, 100, 30)
	send(t, app, prsMsg{gen: app.gen, prs: nil})

	screen := plain(app.render())
	assert.Contains(t, screen, "no open pull requests")
	assert.Contains(t, screen, "0 open PRs")
}

func TestHeaderCountsPullRequests(t *testing.T) {
	app, _, _ := newTestApp(t, 100, 30)
	assert.Contains(t, plain(app.render()), "3 open PRs")

	send(t, app, prsMsg{gen: app.gen, prs: samplePRs()[:1]})
	assert.Contains(t, plain(app.render()), "1 open PR")
}

func TestCheckDurationsAreShown(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	screen := plain(app.render())

	assert.Contains(t, screen, "1m0s", "a finished check shows how long it took")
	assert.Contains(t, screen, "12s")
	assert.Contains(t, screen, "3m0s", "a running check is timed against now")
}

func TestDotPrefersFetchedChecksOverTheRollup(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	pr := samplePRs()[0]
	require.Equal(t, model.StatusSuccess, pr.Rollup)

	// The rollup said success, but the fetched checks contain a failure.
	assert.Equal(t, model.StatusFailure, app.rollupFor(pr))

	app.checks = map[model.Key]checkState{}
	assert.Equal(t, model.StatusSuccess, app.rollupFor(pr), "without checks the rollup is used")
}

// checkLine returns the rendered line containing the named check.
func checkLine(t *testing.T, app *App, name string) string {
	t.Helper()
	for _, line := range lines(app) {
		if strings.Contains(line, " "+name) {
			return line
		}
	}
	t.Fatalf("no line mentions check %q", name)
	return ""
}

func TestDetailPaneScrollsThroughLongCheckLists(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 20)
	key := samplePRs()[0].Key()

	many := make([]model.Check, 30)
	for i := range many {
		many[i] = model.Check{Name: fmt.Sprintf("check-%02d", i), Status: model.StatusSuccess}
	}
	send(t, app, checksMsg{gen: app.gen, key: key, checks: many})

	screen := plain(app.render())
	assert.Contains(t, screen, "check-00")
	assert.NotContains(t, screen, "check-29", "the tail is below the fold")
	assert.Contains(t, screen, "of 30", "the pane reports the window it is showing")

	send(t, app, press("l"))
	send(t, app, press("G"))

	screen = plain(app.render())
	assert.Contains(t, screen, "check-29", "the last check scrolls into view")
	assert.NotContains(t, screen, "check-00")

	for _, line := range lines(app) {
		assert.LessOrEqual(t, ansi.StringWidth(line), 120)
	}
}

func TestHeaderShowsTheVersionWhenSet(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	assert.NotContains(t, plain(app.render()), "v9.9.9")

	app.version = "v9.9.9"
	assert.Contains(t, plain(app.render()), "prutil v9.9.9")
}
