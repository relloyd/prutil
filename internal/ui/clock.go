package ui

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

// minClockGap is the shortest wait the clock will ask for. A timer that fires a
// hair before the wall clock's boundary would otherwise be told to wait a
// millisecond for the rest of it.
const minClockGap = 20 * time.Millisecond

// clockTickMsg is one beat of the clock, which redraws the screen so that the
// elapsed times on it move. seq names the wait that asked for it, so a beat
// from one since replaced, or since stopped by the terminal losing focus, is
// dropped.
type clockTickMsg struct {
	seq int
}

// Update implements tea.Model. It wraps update with the clock, which is not
// part of any one message's handling: whatever a message changed, the clock is
// asked afterwards whether anything on screen now needs it.
//
// Everything on screen that counts is worked out from a.now() when it is drawn,
// so the clock holds no time of its own. All it decides is whether prutil wakes
// up to draw again, which is what lets it stop entirely while the terminal is
// in the background.
func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.FocusMsg:
		a.focused = true
	case tea.BlurMsg:
		a.focused = false
		a.clockSeq++
		a.clockArmed = false
	case tea.KeyPressMsg:
		// Keys reach only the window that has focus, so one that arrives while
		// prutil believes it is in the background means a focus report went
		// missing. Believing the key is the failure that ends. A mouse wheel is
		// not evidence: a terminal can scroll a window it is not focused on,
		// and nothing would then tell prutil to stop.
		a.focused = true
	case clockTickMsg:
		if msg.seq == a.clockSeq {
			a.clockArmed = false
		}
		return a, a.ensureClock()
	}

	next, cmd := a.update(msg)
	return next, tea.Batch(cmd, a.ensureClock())
}

// ensureClock returns the command that wakes prutil at the next moment
// something on screen changes, or nil when nothing is needed.
//
// It runs after every message, so it costs almost nothing in the common case:
// a beat already due within the second cannot be bettered, because every beat
// falls on a whole second.
func (a *App) ensureClock() tea.Cmd {
	if !a.live || !a.focused {
		return nil
	}

	now := a.now()
	if a.clockArmed && a.clockDue.Sub(now) <= time.Second {
		return nil
	}
	due, ok := a.nextBeat(now)
	if !ok || (a.clockArmed && !due.Before(a.clockDue)) {
		return nil
	}

	// Bumping the sequence orphans the beat being replaced, which is the one
	// that was waiting for a minute while a check began to run.
	a.clockSeq++
	seq := a.clockSeq
	a.clockArmed, a.clockDue = true, due
	return tea.Tick(max(due.Sub(now), minClockGap), func(time.Time) tea.Msg {
		return clockTickMsg{seq: seq}
	})
}

// nextBeat is when the screen next needs drawing for the sake of the clocks on
// it, and false when it never does.
//
// Beats are aligned to the wall clock rather than counted from the last one. A
// chain of one-second waits each begins after the last was handled, so it
// slips, and a displayed second is eventually skipped.
func (a *App) nextBeat(now time.Time) (time.Time, bool) {
	switch {
	case a.secondsShown(now):
		return now.Truncate(time.Second).Add(time.Second), true
	case a.minutesShown():
		return now.Truncate(time.Minute).Add(time.Minute), true
	}
	return time.Time{}, false
}

// secondsShown reports whether anything that counts in seconds is, or may be,
// on screen. An overestimate is harmless: the price is a redraw, and a
// pull request scrolled out of sight is not worth working out.
//
// The countdowns stop counting seconds at an hour, because model.HumanDuration
// does.
func (a *App) secondsShown(now time.Time) bool {
	if a.state.ArmedCount() > 0 {
		if next, ok := a.engine.NextDue(); ok && next.Sub(now) < time.Hour {
			return true
		}
	}

	pr, ok := a.selectedPR()
	if !ok {
		return false
	}
	key := pr.Key()
	if a.runtimeOf(key).operation != "" {
		return true
	}
	for _, check := range a.checks[key].checks {
		if check.Running() {
			return true
		}
	}
	return false
}

// minutesShown reports whether anything that counts in minutes is on screen,
// which is every age: how long ago a pull request was updated, opened or
// closed, and when the watcher last did something.
func (a *App) minutesShown() bool {
	return len(a.cur().prs) > 0 || a.adopt.open
}
