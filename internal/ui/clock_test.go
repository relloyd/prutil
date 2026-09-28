package ui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/relloyd/prutil/internal/model"
)

// clockApp is an app with the clock on and nothing yet waiting.
//
// The clock is switched on after the app is built, not through the config,
// because building one runs messages and a test that then drains their
// commands would wait a second, or a minute, for the clock's own. Nothing here
// drains: a clock command is checked by whether there is one and what it is
// due at, never run.
func clockApp(t *testing.T) *App {
	t.Helper()

	app, _, _ := newTestApp(t, 120, 40)
	app.live = true
	return app
}

// resize is a message that changes nothing, so it produces no command of its
// own and whatever comes back is the clock's.
func resize() tea.Msg { return tea.WindowSizeMsg{Width: 120, Height: 40} }

func TestTheViewOnlyAsksForFocusReportsWhenTheClockIsOn(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	assert.False(t, app.View().ReportFocus, "with no clock there is nothing to stand down")

	app.live = true
	assert.True(t, app.View().ReportFocus)
}

func TestWithoutTheClockNothingIsEverWaitedOn(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)

	send(t, app, press("w"))
	send(t, app, tea.BlurMsg{})
	send(t, app, tea.FocusMsg{})

	assert.False(t, app.clockArmed)
}

func TestTheClockBeatsAtTheGranularityOfWhatIsOnScreen(t *testing.T) {
	key42 := model.Key{Repo: "relloyd/prutil", Number: 42}
	finished := []model.Check{{
		Name: "test", Status: model.StatusSuccess,
		StartedAt: testNow.Add(-10 * time.Minute), CompletedAt: testNow.Add(-9 * time.Minute),
	}}

	tests := []struct {
		name  string
		setup func(t *testing.T, app *App)
		// want is when the next beat falls, and the zero time when there is none.
		want time.Time
	}{
		{
			name:  "A check that is still running counts in seconds.",
			setup: func(*testing.T, *App) {},
			want:  testNow.Add(time.Second),
		},
		{
			name: "A check that has finished no longer does, and the ages alone count in minutes.",
			setup: func(_ *testing.T, app *App) {
				app.checks[key42] = checkState{checks: finished, loaded: true}
			},
			want: testNow.Add(time.Minute),
		},
		{
			name: "A pull request with nothing running beside it counts in minutes.",
			setup: func(t *testing.T, app *App) {
				send(t, app, press("j"))
			},
			want: testNow.Add(time.Minute),
		},
		{
			name: "Work in flight on the selected pull request counts in seconds.",
			setup: func(t *testing.T, app *App) {
				send(t, app, press("j"))
				pr, ok := app.selectedPR()
				require.True(t, ok)
				app.setWatchOperation(pr.Key(), "handing feedback to an agent")
			},
			want: testNow.Add(time.Second),
		},
		{
			name: "A watched pull request's countdown counts in seconds wherever the cursor is.",
			setup: func(t *testing.T, app *App) {
				send(t, app, press("w"))
				send(t, app, press("j"))
			},
			want: testNow.Add(time.Second),
		},
		{
			name: "A countdown of more than an hour is drawn in minutes, so it is beaten in them.",
			setup: func(t *testing.T, app *App) {
				// Deferred rather than polled: a poll pumps the watcher's own wait,
				// which here would be two hours.
				send(t, app, press("w"))
				send(t, app, press("j"))
				app.engine.Defer([]model.Key{key42}, 2*time.Hour, app.now())
				next, ok := app.engine.NextDue()
				require.True(t, ok)
				require.Greater(t, next.Sub(app.now()), time.Hour, "the fixture is a long wait")
			},
			want: testNow.Add(time.Minute),
		},
		{
			name: "Nothing on screen that counts means no beat at all.",
			setup: func(_ *testing.T, app *App) {
				app.views[viewOpen].prs = nil
			},
		},
		{
			name: "The adopt pane's ages count in minutes even over an empty list.",
			setup: func(_ *testing.T, app *App) {
				app.views[viewOpen].prs = nil
				app.adopt.open = true
			},
			want: testNow.Add(time.Minute),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, _, _ := newTestApp(t, 120, 40)
			tt.setup(t, app)

			got, ok := app.nextBeat(app.now())

			require.Equal(t, !tt.want.IsZero(), ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestBeatsFallOnTheWallClockWhereverInASecondTheyAreAskedFor(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)

	for _, offset := range []time.Duration{0, 250 * time.Millisecond, 999 * time.Millisecond} {
		got, ok := app.nextBeat(testNow.Add(offset))

		require.True(t, ok)
		assert.Equal(t, testNow.Add(time.Second), got,
			"a second after the last beat would slip, and eventually skip a second on screen")
	}

	send(t, app, press("j"))
	got, ok := app.nextBeat(testNow.Add(30*time.Second + 250*time.Millisecond))
	require.True(t, ok)
	assert.Equal(t, testNow.Add(time.Minute), got, "minutes are aligned the same way")
}

func TestLosingFocusStopsTheClock(t *testing.T) {
	app := clockApp(t)
	require.NotNil(t, send(t, app, resize()), "there is a running check on screen to count")
	require.True(t, app.clockArmed)

	assert.Nil(t, send(t, app, tea.BlurMsg{}))
	assert.False(t, app.clockArmed)
	assert.Nil(t, send(t, app, resize()), "nothing asks for a beat while the terminal is in the background")
}

func TestRegainingFocusStartsTheClockAgain(t *testing.T) {
	app := clockApp(t)
	send(t, app, tea.BlurMsg{})

	assert.NotNil(t, send(t, app, tea.FocusMsg{}))
	assert.True(t, app.clockArmed)
	assert.Equal(t, testNow.Add(time.Second), app.clockDue)
}

func TestABeatFromBeforeALostFocusIsDropped(t *testing.T) {
	app := clockApp(t)
	send(t, app, resize())
	stale := app.clockSeq
	send(t, app, tea.BlurMsg{})
	send(t, app, tea.FocusMsg{})
	require.NotEqual(t, stale, app.clockSeq)

	assert.Nil(t, send(t, app, clockTickMsg{seq: stale}))
	assert.True(t, app.clockArmed, "the wait from the refocus is still the one in flight")
}

func TestABeatThatArrivesInTheBackgroundDoesNotRestartTheClock(t *testing.T) {
	app := clockApp(t)
	send(t, app, resize())
	seq := app.clockSeq
	send(t, app, tea.BlurMsg{})

	assert.Nil(t, send(t, app, clockTickMsg{seq: seq}))
	assert.Nil(t, send(t, app, clockTickMsg{seq: app.clockSeq}))
	assert.False(t, app.clockArmed)
}

func TestEachBeatAsksForTheNext(t *testing.T) {
	app := clockApp(t)
	send(t, app, resize())
	require.Equal(t, testNow.Add(time.Second), app.clockDue)

	advance(app, time.Second)
	assert.NotNil(t, send(t, app, clockTickMsg{seq: app.clockSeq}))
	assert.Equal(t, testNow.Add(2*time.Second), app.clockDue)
}

func TestOnlyOneBeatIsWaitedOnAtATime(t *testing.T) {
	app := clockApp(t)

	assert.NotNil(t, send(t, app, resize()))
	assert.Nil(t, send(t, app, resize()), "a message that changes nothing about what is on screen adds no wait")
}

func TestASoonerBeatReplacesALaterOne(t *testing.T) {
	app := clockApp(t)
	send(t, app, press("j"))
	require.Equal(t, testNow.Add(time.Minute), app.clockDue, "only ages are on screen")
	minute := app.clockSeq

	pr, ok := app.selectedPR()
	require.True(t, ok)
	app.setWatchOperation(pr.Key(), "handing feedback to an agent")

	assert.NotNil(t, send(t, app, resize()))
	assert.Equal(t, testNow.Add(time.Second), app.clockDue,
		"the reader would otherwise wait a minute for the first second of an operation")
	assert.NotEqual(t, minute, app.clockSeq)

	assert.Nil(t, send(t, app, clockTickMsg{seq: minute}), "the replaced wait is dropped when it arrives")
	assert.True(t, app.clockArmed)
}

func TestAKeyPressMeansTheTerminalHasFocus(t *testing.T) {
	app := clockApp(t)
	send(t, app, resize())
	send(t, app, tea.BlurMsg{})
	require.False(t, app.focused)

	send(t, app, press("j"))

	assert.True(t, app.focused, "a focus report went missing, and a key is proof of it")
	assert.True(t, app.clockArmed)
}

func TestAMouseWheelIsNotProofOfFocus(t *testing.T) {
	app := clockApp(t)
	send(t, app, resize())
	send(t, app, tea.BlurMsg{})

	send(t, app, tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: 4, Y: headerHeight + 1})

	assert.False(t, app.focused,
		"a terminal can scroll a window it is not focused on, and nothing would then say to stop")
	assert.False(t, app.clockArmed)
}

func TestAnOperationThatIsTakingAWhileSaysHowLong(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	send(t, app, press("w"))
	pr, ok := app.selectedPR()
	require.True(t, ok)
	key := pr.Key()
	row := func() string {
		got, ok := app.currentRow(app.watchFactsOf(pr), true)
		require.True(t, ok)
		return got.text
	}

	app.setWatchOperation(key, "handing feedback to an agent")
	assert.Equal(t, "operation: handing feedback to an agent", row(),
		"most operations are over before there is anything to read")

	advance(app, 2*time.Minute+10*time.Second)
	assert.Equal(t, "operation: handing feedback to an agent · 2m10s", row())
	assert.Contains(t, plain(app.render()), "handing feedback to an agent · 2m10s",
		"the compact section says it too, not just the page")
}

func TestRestatingAnOperationDoesNotRestartItsClock(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	pr, ok := app.selectedPR()
	require.True(t, ok)
	key := pr.Key()
	row := func() string {
		got, ok := app.currentRow(app.watchFactsOf(pr), true)
		require.True(t, ok)
		return got.text
	}

	app.setWatchOperation(key, "handing feedback to an agent")
	advance(app, 30*time.Second)
	app.setWatchOperation(key, "handing feedback to an agent")
	advance(app, time.Second)
	assert.Equal(t, "operation: handing feedback to an agent · 31s", row())

	app.setWatchOperation(key, "reading review feedback")
	assert.Equal(t, "operation: reading review feedback", row(), "a different operation starts from nothing")
}
