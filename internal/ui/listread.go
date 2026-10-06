package ui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/relloyd/prutil/internal/home"
)

// The open list is re-read in the background, every watch.list_interval, for
// two things: auto-watch, which needs the search to find a pull request opened
// since the list was last read, and the desktop notifications, which need only
// a fresh reading of the pull requests already listed. They used to be two
// timers with two settings, which read to the reader as two jobs when they are
// one: one wait, and at its end whichever read the features switched on need.
// The search answers both, because every load of the open list is also a
// reading for the notifications, so the cheaper read runs only when auto-watch
// is off.
//
// Watched pull requests are not read here. The watcher polls them on its own
// schedule, the POLL TIMING settings.

// listReadTickMsg is the wait before the next background read running out.
// seq names the wait, so one a later load replaced is dropped.
type listReadTickMsg struct {
	seq int
}

// listReadWanted reports whether anything needs the open list re-read.
func (a *App) listReadWanted() bool {
	return a.autoWatching() || a.notificationsWanted()
}

// listInterval is the wait between two background reads of the open list.
func (a *App) listInterval() time.Duration {
	if d := a.homeCfg.Watch.ListInterval.Duration(); d > 0 {
		return d
	}
	return home.DefaultListInterval
}

// scheduleListRead starts the wait before the next background read, replacing
// any wait already under way.
//
// It is called after every load of the open list, whoever asked for it, so
// that r or a reads the list and restarts the wait rather than a read landing
// straight after them; after every cheap read; and whenever a setting it
// depends on changes. Bumping the sequence is what drops the wait it replaces,
// and with nothing wanting a read, what ends the run.
func (a *App) scheduleListRead() tea.Cmd {
	a.listReadSeq++
	a.listReadWaiting = false
	if !a.listReadWanted() {
		return nil
	}
	a.listReadWaiting = true
	seq := a.listReadSeq
	return tea.Tick(a.listInterval(), func(time.Time) tea.Msg { return listReadTickMsg{seq: seq} })
}

// listReadTick runs whichever read the wait was for: the search while
// auto-watch is on, since only it finds a new pull request, and otherwise the
// cheap read of the pull requests already listed.
func (a *App) listReadTick(msg listReadTickMsg) tea.Cmd {
	if msg.seq != a.listReadSeq {
		return nil
	}
	a.listReadWaiting = false
	switch {
	case a.autoWatching():
		if a.views[viewOpen].loading {
			// The load in flight schedules the next wait when it lands.
			return nil
		}
		return a.withSpinner(a.load(viewOpen))
	case a.notificationsWanted():
		return a.pollNotifications()
	}
	return nil
}
