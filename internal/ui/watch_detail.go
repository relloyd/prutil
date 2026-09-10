package ui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/relloyd/prutil/internal/home"
	"github.com/relloyd/prutil/internal/model"
	"github.com/relloyd/prutil/internal/watch"
)

// watchFacts is everything the two watch renderers say, read once.
//
// They phrase it differently, because one has a few lines beside the checks
// and the other has a page: "checks running · next in 5m · every 2m" against a
// line each for state, cadence and next check. What they must not differ on is
// which facts there are and whether there are any, which is what this gathers.
type watchFacts struct {
	key    model.Key
	armed  bool
	status watch.Status
	// operation is the work in flight, and open the review threads still
	// waiting. Only one of them is shown: work in flight is the newer news.
	operation string
	open      int
	hasOpen   bool
	// events are this session's watcher activity, oldest first, as recorded.
	events  []watchActivity
	history handoffHistoryState
}

// watchFactsOf reads the watcher state for one pull request. It allocates
// nothing: every field is a map lookup or a small copy, which matters because
// anything reports through it on every key press.
func (a *App) watchFactsOf(pr model.PullRequest) watchFacts {
	key := pr.Key()
	status, armed := a.engine.Status(key)
	open, hasOpen := a.feedback[key]
	return watchFacts{
		key:       key,
		armed:     armed,
		status:    status,
		operation: a.watching[key],
		open:      open,
		hasOpen:   hasOpen,
		events:    a.activity[key],
		history:   a.handoffHistory[key],
	}
}

// anything reports whether the WATCH section has something to show.
//
// A history read still in flight deliberately does not count. Selecting a pull
// request starts one, so counting it would open the section on every selection
// and shut it again a moment later for the ordinary case of a pull request
// nothing has ever been handed off for, moving the checks beneath it twice for
// nothing. A section only appears once it has something to say.
func (f watchFacts) anything() bool {
	return f.armed ||
		len(f.events) > 0 ||
		f.history.err != nil ||
		len(f.history.handoffs) > 0
}

// hasWatchSection reports whether the WATCH section has anything to say for
// the selected pull request.
func (a *App) hasWatchSection(pr model.PullRequest) bool {
	return a.watchFactsOf(pr).anything()
}

// watchRow is one line of the watch pane before it is styled or fitted to a
// width.
//
// Rows rather than finished strings, because the expanded page is measured on
// every key press while it is open, and measuring should be counting a slice
// rather than styling and truncating a screenful of text to throw it away.
type watchRow struct {
	text  string
	style lipgloss.Style
	// blank is a spacer, which carries no text and takes no styling.
	blank bool
	// heading marks a section heading, which brings its own indent and can
	// carry the selection bar. It is rendered through sectionHeading rather
	// than styled here, so that no row is ever truncated after the escape
	// codes have gone in.
	heading bool
}

// renderWatchRow turns one row into a line of at most width columns.
func (a *App) renderWatchRow(row watchRow, width int) string {
	switch {
	case row.blank:
		return ""
	case row.heading:
		return a.sectionHeading(row.text, false)
	default:
		return row.style.Render(truncatePlain(row.text, width))
	}
}

// renderWatchRows turns a run of rows into lines.
func (a *App) renderWatchRows(rows []watchRow, width int) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, a.renderWatchRow(row, width))
	}
	return out
}

// watchDetail renders the compact WATCH section that sits above the checks,
// stopping at budget lines so that a check heading and a check still fit.
func (a *App) watchDetail(pr model.PullRequest, width, budget int, selected bool) []string {
	facts := a.watchFactsOf(pr)
	if budget < 2 || !facts.anything() {
		return nil
	}

	lines := []string{a.sectionHeading("WATCH", selected)}
	lines = append(lines, a.renderWatchRow(a.compactState(facts), width))

	for _, row := range a.compactRows(facts) {
		if len(lines) >= budget {
			break
		}
		lines = append(lines, a.renderWatchRow(row, width))
	}
	return lines
}

// compactState is the one line the compact section spends on the schedule,
// packing what the expanded page gives a line each.
func (a *App) compactState(f watchFacts) watchRow {
	if !f.armed {
		return watchRow{text: "not watching · manual handoff activity", style: a.styles.Meta}
	}

	state := f.status.Tier.String()
	switch f.status.Tier {
	case watch.TierDormant:
		state += " · refresh to check again"
	default:
		state += " · " + a.nextCheck(f, "next in ", "due now")
		state += " · every " + model.HumanDuration(f.status.Interval)
	}
	switch {
	case !f.status.SnapshotSeen:
		state += " · waiting for first check"
	case f.status.PollsUntilPrecise > 0:
		state += fmt.Sprintf(" · review check in %d", f.status.PollsUntilPrecise)
	}
	return watchRow{text: state, style: a.styles.Watch}
}

// compactRows are everything the compact section shows beneath its state line,
// in the order it gives them up as the budget runs out: what is happening now,
// then the two most recent events, then the durable history.
func (a *App) compactRows(f watchFacts) []watchRow {
	rows := make([]watchRow, 0, 6)
	if row, ok := a.currentRow(f, false); ok {
		rows = append(rows, row)
	}
	rows = append(rows, a.eventRows(f, 2)...)

	switch {
	case f.history.loading:
		rows = append(rows, watchRow{text: "loading handoff history…", style: a.styles.Meta})
	case f.history.err != nil:
		rows = append(rows, a.historyErrorRow(f))
	default:
		for _, handoff := range f.history.handoffs {
			rows = append(rows, watchRow{text: handoffHistoryLine(a.now(), handoff), style: a.styles.Muted})
		}
	}
	return rows
}

// watchPageRows is the expanded WATCH page, one fact to a line.
func (a *App) watchPageRows(pr model.PullRequest) []watchRow {
	f := a.watchFactsOf(pr)
	if !f.anything() {
		return nil
	}

	rows := []watchRow{
		{text: "WATCH", heading: true},
		{text: f.key.String(), style: a.styles.Repo},
		{blank: true},
	}

	if !f.armed {
		rows = append(rows, watchRow{text: "state: not watching", style: a.styles.Meta})
	} else {
		rows = append(rows,
			watchRow{text: "state: " + f.status.Tier.String(), style: a.styles.Watch},
			watchRow{text: "cadence: every " + model.HumanDuration(f.status.Interval), style: a.styles.Meta},
			watchRow{text: "next check: " + a.nextCheck(f, "in ", "due now"), style: a.styles.Meta},
			watchRow{text: "snapshot: " + snapshotState(f), style: a.styles.Meta},
		)
		if f.status.PollsUntilPrecise > 0 {
			rows = append(rows, watchRow{
				text:  fmt.Sprintf("precise review: in %d polls", f.status.PollsUntilPrecise),
				style: a.styles.Meta,
			})
		}
	}
	if row, ok := a.currentRow(f, true); ok {
		rows = append(rows, row)
	}

	rows = append(rows, watchRow{blank: true}, watchRow{text: "ACTIVITY", heading: true})
	if len(f.events) == 0 {
		rows = append(rows, watchRow{text: "none this session", style: a.styles.Meta})
	} else {
		rows = append(rows, a.eventRows(f, len(f.events))...)
	}

	rows = append(rows, watchRow{blank: true}, watchRow{text: "HANDOFFS", heading: true})
	switch {
	case f.history.loading:
		rows = append(rows, watchRow{text: "loading handoff history…", style: a.styles.Meta})
	case f.history.err != nil:
		rows = append(rows, a.historyErrorRow(f))
	case len(f.history.handoffs) == 0:
		rows = append(rows, watchRow{text: "none recorded", style: a.styles.Meta})
	default:
		for _, handoff := range f.history.handoffs {
			rows = append(rows, watchRow{text: handoffHistoryLine(a.now(), handoff), style: a.styles.Muted})
			if handoff.Workspace != "" {
				rows = append(rows, watchRow{text: "  workspace: " + handoff.Workspace, style: a.styles.Muted})
			}
			if handoff.Tab != "" {
				rows = append(rows, watchRow{text: "  tab: " + handoff.Tab, style: a.styles.Muted})
			}
		}
	}
	return rows
}

// renderWatchPage draws the expanded page, scrolled to watchOffset.
func (a *App) renderWatchPage(pr model.PullRequest, width, height int) []string {
	rows := a.watchPageRows(pr)
	if len(rows) == 0 {
		return a.centeredNotice("nothing to show for WATCH", width, a.styles.Meta)
	}

	window := max(height-1, 1)
	start := min(a.watchOffset, max(len(rows)-1, 0))
	end := min(start+window, len(rows))
	out := a.renderWatchRows(rows[start:end], width)
	if end < len(rows) || start > 0 {
		out = append(out, a.styles.Muted.Render(fmt.Sprintf("  %d–%d of %d", start+1, end, len(rows))))
	}
	return out
}

// nextCheck says when the next poll falls due, with the wording its caller
// needs in front of it.
func (a *App) nextCheck(f watchFacts, prefix, due string) string {
	if f.status.NextDue.After(a.now()) {
		return prefix + model.HumanDuration(f.status.NextDue.Sub(a.now()))
	}
	return due
}

// snapshotState says whether the watcher has a reading to compare against yet.
func snapshotState(f watchFacts) string {
	if f.status.SnapshotSeen {
		return "seen"
	}
	return "waiting for first check"
}

// currentRow is what is happening now, or how much feedback is waiting when
// nothing is.
//
// labelled says which of the two callers is asking. The page has a line and a
// full width to spend, so it names what it is showing; the compact section
// sits beside the checks and spends its width on the fact instead, leaving the
// colour to say which fact it is.
func (a *App) currentRow(f watchFacts, labelled bool) (watchRow, bool) {
	label := func(prefix, text string) string {
		if labelled {
			return prefix + text
		}
		return text
	}

	switch {
	case f.operation != "":
		return watchRow{text: label("operation: ", f.operation), style: a.styles.Status}, true
	case f.hasOpen:
		return watchRow{
			text:  label("feedback: ", fmt.Sprintf("%d open %s", f.open, plural(f.open, "thread"))),
			style: a.styles.Meta,
		}, true
	}
	return watchRow{}, false
}

// eventRows are the most recent watcher events, newest first, at most limit of
// them.
func (a *App) eventRows(f watchFacts, limit int) []watchRow {
	rows := make([]watchRow, 0, min(limit, len(f.events)))
	for i := len(f.events) - 1; i >= 0 && len(rows) < limit; i-- {
		event := f.events[i]
		rows = append(rows, watchRow{
			text:  watchAge(a.now(), event.at) + " · " + event.text,
			style: a.styles.Muted,
		})
	}
	return rows
}

// historyErrorRow explains a durable history that could not be read.
func (a *App) historyErrorRow(f watchFacts) watchRow {
	return watchRow{text: "handoff history unavailable: " + f.history.err.Error(), style: a.styles.Error}
}

// handoffHistoryLine keeps one durable handoff record useful in a narrow pane.
func handoffHistoryLine(now time.Time, handoff home.Handoff) string {
	parts := []string{watchAge(now, handoff.At), handoff.Outcome}
	if handoff.Provisioned {
		parts = append(parts, "provisioned")
	}
	target := strings.TrimSpace(handoff.Kind + " " + handoff.Target)
	if target != "" {
		parts = append(parts, target)
	}
	if handoff.Detail != "" {
		parts = append(parts, handoff.Detail)
	}
	return strings.Join(parts, " · ")
}

// watchAge reads an age the way a sentence wants it.
func watchAge(now, at time.Time) string {
	age := model.HumanAge(now.Sub(at))
	if age == "just now" {
		return age
	}
	return age + " ago"
}
