package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/relloyd/prutil/internal/desktop"
	"github.com/relloyd/prutil/internal/home"
	"github.com/relloyd/prutil/internal/model"
)

// maxToasts is how many notifications one reading raises separately. Past it,
// one notification sums the changes up: a laptop opened after a weekend
// should not bury the screen.
const maxToasts = 3

// prFacts is what a notification is raised on, as last read for one pull
// request. A new notification adds the fact it needs here.
type prFacts struct {
	approved bool
	// passed is every check on head having passed. head is kept with it so
	// that a push whose checks pass between two readings, and so is never
	// seen pending, is still news.
	passed bool
	head   string
}

func factsOfPR(pr model.PullRequest) prFacts {
	return prFacts{approved: pr.Approved(), passed: pr.Rollup == model.StatusSuccess, head: pr.HeadOID}
}

func factsOfSnapshot(s model.Snapshot) prFacts {
	return prFacts{approved: s.Approved(), passed: s.Rollup == model.StatusSuccess, head: s.HeadOID}
}

// notification is one kind of change the reader can be told about: how the
// settings pane names it, how the notification says it, and how to recognise
// it between two readings. Every home.NotificationEvent has exactly one.
type notification struct {
	event home.NotificationEvent
	// setting and detail are the settings pane's row and its explanation.
	setting string
	detail  string
	// headline follows the pull request's name: "acme/widgets#7 approved".
	headline string
	fired    func(before, after prFacts) bool
}

var notifications = []notification{
	{
		event:   home.NotifyApproved,
		setting: "Pull request approved",
		detail: "When one of your open pull requests is approved: its review decision turns to approved or, " +
			"in a repository without review rules, it gets its first approval.",
		headline: "approved",
		fired:    func(before, after prFacts) bool { return !before.approved && after.approved },
	},
	{
		event:   home.NotifyChecksPassed,
		setting: "Checks passed",
		detail: "When every check on one of your open pull requests passes: once per pushed commit, " +
			"after the checks were seen running or failing, or on a new commit.",
		headline: "checks passed",
		fired: func(before, after prFacts) bool {
			return after.passed && (!before.passed || before.head != after.head)
		},
	},
}

// reading is one fresh look at a pull request, and when it was asked for.
type reading struct {
	key   model.Key
	facts prFacts
	at    time.Time
}

// change is a notification one reading called for.
type change struct {
	key  model.Key
	kind notification
}

// readingsOfPRs turns a freshly loaded open list into readings.
func readingsOfPRs(prs []model.PullRequest, at time.Time) []reading {
	out := make([]reading, 0, len(prs))
	for _, pr := range prs {
		out = append(out, reading{key: pr.Key(), facts: factsOfPR(pr), at: at})
	}
	return out
}

// readingsOfSnapshots turns a batch of tripwire replies into readings.
func readingsOfSnapshots(snaps []model.Snapshot, at time.Time) []reading {
	out := make([]reading, 0, len(snaps))
	for _, snap := range snaps {
		if snap.Key.Repo != "" {
			out = append(out, reading{key: snap.Key, facts: factsOfSnapshot(snap), at: at})
		}
	}
	return out
}

// notice remembers fresh readings and raises the notifications the changes
// since the last ones call for.
//
// The first reading of a pull request is only remembered. It says where the
// pull request stands, which the reader can see on screen, not that anything
// happened. Every reading is remembered whether or not a notification is on,
// so that turning one on does not announce something that happened before.
//
// Readings come from the list, the watcher and the notification poll, whose
// replies can arrive out of order. One asked for before the reading already
// held is older news and is ignored, or a slow list load could undo an
// approval the poll had just seen and let the next poll announce it again.
func (a *App) notice(readings []reading) tea.Cmd {
	var changes []change
	for _, r := range readings {
		entry := a.mutate(r.key)
		if entry.hasFacts && r.at.Before(entry.factsAt) {
			continue
		}
		before, seen := entry.facts, entry.hasFacts
		entry.facts, entry.factsAt, entry.hasFacts = r.facts, r.at, true
		if !seen {
			continue
		}
		for _, kind := range notifications {
			if kind.fired(before, r.facts) && a.homeCfg.Notifications.Enabled(kind.event) {
				changes = append(changes, change{key: r.key, kind: kind})
			}
		}
	}
	return a.announce(changes)
}

// announce tells the reader about changes: on the status line, in the WATCH
// activity of a watched pull request, and as desktop notifications.
func (a *App) announce(changes []change) tea.Cmd {
	if len(changes) == 0 {
		return nil
	}

	toasts := make([]desktop.Notification, 0, len(changes))
	names := make([]string, 0, len(changes))
	for _, c := range changes {
		name := c.key.String() + " " + c.kind.headline
		names = append(names, name)
		title := ""
		if pr, ok := a.prByKey(c.key); ok {
			title = pr.Title
		}
		toasts = append(toasts, desktop.Notification{Title: name, Body: title})
		if a.armed(c.key) {
			activity := c.kind.headline
			if a.notifier != nil {
				activity += "; desktop notification raised"
			}
			a.recordWatchActivity(c.key, activity)
		}
	}

	line := names[0]
	if len(changes) > 1 {
		line = fmt.Sprintf("%d pull requests changed: %s", len(changes), strings.Join(names, ", "))
	}
	if len(toasts) > maxToasts {
		toasts = []desktop.Notification{{
			Title: fmt.Sprintf("%d pull requests changed", len(changes)),
			Body:  strings.Join(names, ", "),
		}}
	}

	if a.notifier == nil {
		return status(line)
	}
	notifier := a.notifier
	return tea.Batch(status(line), func() tea.Msg {
		for _, toast := range toasts {
			if err := notifier.Notify(toast); err != nil {
				return statusMsg("could not show a desktop notification: " + err.Error())
			}
		}
		return nil
	})
}

// notificationsWanted reports whether the open pull requests should be read
// for notifications: something can show them, one is on, and there is a list
// to read.
func (a *App) notificationsWanted() bool {
	return a.notifier != nil && a.homeCfg.Notifications.Any() && len(a.views[viewOpen].prs) > 0
}

// notifyInterval is the gap between two reads of the open pull requests.
func (a *App) notifyInterval() time.Duration {
	if d := a.homeCfg.Notifications.Interval.Duration(); d > 0 {
		return d
	}
	return home.DefaultNotificationInterval
}

// scheduleNotifications starts the wait before the next read, unless one is
// already under way. Only one wait or read is ever outstanding, so calling it
// after every list load and every toggle is safe.
func (a *App) scheduleNotifications() tea.Cmd {
	if a.notifyPending || !a.notificationsWanted() {
		return nil
	}
	a.notifyPending = true
	return tea.Tick(a.notifyInterval(), func(time.Time) tea.Msg { return notifyTickMsg{} })
}

// pollNotifications reads every open pull request with the watcher's cheap
// query: one request per hundred of them, whatever repositories they are in.
// A notification turned off since the wait began ends the run here.
func (a *App) pollNotifications() tea.Cmd {
	a.notifyPending = false
	if !a.notificationsWanted() {
		return nil
	}

	prs := a.views[viewOpen].prs
	byID := make(map[string]model.Key, len(prs))
	ids := make([]string, 0, len(prs))
	for _, pr := range prs {
		if pr.NodeID != "" {
			byID[pr.NodeID] = pr.Key()
			ids = append(ids, pr.NodeID)
		}
	}
	if len(ids) == 0 {
		return nil
	}

	a.notifyPending = true
	client, at := a.client, a.now()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		defer cancel()

		snaps, err := client.WatchSnapshot(ctx, ids)
		if err != nil {
			return notifyPollMsg{err: err}
		}
		for i := range snaps {
			snaps[i].Key = byID[snaps[i].NodeID]
		}
		return notifyPollMsg{at: at, snaps: snaps}
	}
}

// applyNotifyPoll takes a read's reply and waits for the next one. A failed
// read is kept for the settings pane rather than put on the status line: on a
// laptop that has lost its network it would otherwise say so every couple of
// minutes, and the next read tries again anyway.
func (a *App) applyNotifyPoll(msg notifyPollMsg) tea.Cmd {
	a.notifyPending = false
	a.notifyErr = msg.err
	var announce tea.Cmd
	if msg.err == nil {
		announce = a.notice(readingsOfSnapshots(msg.snaps, msg.at))
	}
	return tea.Batch(announce, a.scheduleNotifications())
}

// notifyTickMsg says the wait before the next notification read is over.
type notifyTickMsg struct{}

// notifyPollMsg carries one notification read.
type notifyPollMsg struct {
	at    time.Time
	snaps []model.Snapshot
	err   error
}
