package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// autoWatchBurst is the most pull requests one load arms automatically. The
// rest wait for the next load. A watched pull request with failing checks can
// start an agent, and a burst of new pull requests, a stack of them opened at
// once, should not be a burst of agents.
const autoWatchBurst = 5

// autoWatching reports whether new pull requests are being armed on their own.
// Without a store nothing can be armed, so there is nothing to look for.
func (a *App) autoWatching() bool {
	return a.homeCfg.Watch.AutoWatch && a.store != nil
}

// setAutoWatch follows the setting being switched on or off. Switching it on is
// the moment new is measured from, so a pull request opened while it was off is
// not armed the moment it is switched back on. The background read is
// rescheduled either way: it is the search while auto-watch is on, and the
// cheaper read, or nothing, while it is off.
func (a *App) setAutoWatch() tea.Cmd {
	if !a.autoWatching() {
		a.state.StopAutoWatch()
	} else {
		a.state.StartAutoWatch(a.now())
	}
	next := a.scheduleListRead()
	if err := a.saveState(); err != nil {
		return tea.Batch(next, status(err.Error()))
	}
	return next
}

// applyAutoWatch arms the reader's new pull requests in a freshly loaded open
// list. The next look is noticeAfterLoad's to schedule.
//
// A pull request is new when the reader opened it, the list's own search found
// it, it was created after auto-watch was switched on, and auto-watch has not
// armed it before. The last is what makes stopping a watch stick: the next load
// finds the pull request already considered and leaves it alone. A draft waits
// until it is ready for review, unless the reader asked for drafts too.
//
// Nothing is armed until prutil knows who the reader is, because an author it
// cannot compare against is not known to be them.
func (a *App) applyAutoWatch(msg prsMsg) tea.Cmd {
	if msg.view != viewOpen || msg.own == nil || !a.autoWatching() {
		return nil
	}
	if a.state.AutoWatch == nil {
		// Switched on in the file rather than in the pane: from now, then.
		a.state.StartAutoWatch(a.now())
		if err := a.saveState(); err != nil {
			return status(err.Error())
		}
		return nil
	}
	viewer := a.viewer
	if viewer == "" {
		return nil
	}

	aw := a.state.AutoWatch
	changed := false
	var armed []string
	for _, pr := range msg.prs {
		key := pr.Key()
		name := key.String()
		if !msg.own[key] || !strings.EqualFold(pr.Author, viewer) ||
			!pr.CreatedAt.After(aw.Since) || aw.Seen(name) {
			continue
		}
		if a.armed(key) {
			// The reader got there first. It is still considered, so that
			// stopping it later is not undone.
			aw.Consider(name, pr.CreatedAt)
			changed = true
			continue
		}
		if pr.IsDraft && !a.homeCfg.Watch.AutoWatchDrafts {
			continue
		}
		if len(armed) >= autoWatchBurst {
			continue
		}
		a.state.SetArmed(name, true)
		aw.Consider(name, pr.CreatedAt)
		a.recordWatchActivity(key, "started watching automatically: a new pull request of yours")
		armed = append(armed, name)
		changed = true
	}

	// Only a list that holds every open pull request says which have closed.
	// One cut short by -limit would forget pull requests that are still open,
	// and a forgotten one that the reader stopped watching would be armed
	// again.
	if a.limit > 0 && len(msg.own) < a.limit {
		open := make(map[string]bool, len(msg.own))
		for key := range msg.own {
			open[key.String()] = true
		}
		before := len(aw.Considered)
		aw.Forget(open)
		changed = changed || len(aw.Considered) != before
	}

	if !changed {
		return nil
	}
	if err := a.saveState(); err != nil {
		return status(err.Error())
	}
	if len(armed) == 0 {
		return nil
	}
	return tea.Batch(a.syncWatch(), status(fmt.Sprintf("watching %d new %s automatically: %s",
		len(armed), plural(len(armed), "pull request"), strings.Join(armed, ", "))))
}

// autoWatchNote is what the header says while auto-watch is on, so that a
// reader who finds a pull request watched knows why.
func (a *App) autoWatchNote() string {
	if !a.autoWatching() {
		return ""
	}
	return " · new PR watching"
}

// readViewer reports whether the open list's load should ask who the reader
// is: auto-watch needs it before any review thread has been read.
func (a *App) readViewer() bool {
	return a.autoWatching() && a.viewer == ""
}
