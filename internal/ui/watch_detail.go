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

// watchDetail renders the selected pull request's current watcher state and
// recent in-session activity, while leaving room for the checks below it.
func (a *App) watchDetail(pr model.PullRequest, width, budget int, selected bool) []string {
	status, armed := a.engine.Status(pr.Key())
	events := a.activity[pr.Key()]
	history := a.handoffHistory[pr.Key()]
	if budget < 2 || !a.hasWatchSection(pr) {
		return nil
	}

	lines := []string{a.sectionHeading("WATCH", selected)}
	if !armed {
		lines = append(lines, a.styles.Meta.Render("not watching · manual handoff activity"))
	} else {
		state := status.Tier.String()
		switch status.Tier {
		case watch.TierDormant:
			state += " · refresh to check again"
		default:
			if status.NextDue.After(a.now()) {
				state += " · next in " + model.HumanDuration(status.NextDue.Sub(a.now()))
			} else {
				state += " · due now"
			}
			state += " · every " + model.HumanDuration(status.Interval)
		}
		if !status.SnapshotSeen {
			state += " · waiting for first check"
		} else if status.PollsUntilPrecise > 0 {
			state += fmt.Sprintf(" · review check in %d", status.PollsUntilPrecise)
		}
		lines = append(lines, a.styles.Watch.Render(truncatePlain(state, width)))
	}

	if len(lines) < budget {
		if current := a.watching[pr.Key()]; current != "" {
			lines = append(lines, a.styles.Status.Render(truncatePlain(current, width)))
		} else if open, ok := a.feedback[pr.Key()]; ok {
			lines = append(lines, a.styles.Meta.Render(fmt.Sprintf("%d open %s", open, plural(open, "thread"))))
		}
	}

	for i := len(events) - 1; i >= 0 && len(lines) < budget && i >= len(events)-2; i-- {
		event := events[i]
		text := watchAge(a.now(), event.at) + " · " + event.text
		lines = append(lines, a.styles.Muted.Render(truncatePlain(text, width)))
	}

	if len(lines) >= budget {
		return lines
	}
	switch {
	case history.loading:
		lines = append(lines, a.styles.Meta.Render("loading handoff history…"))
	case history.err != nil:
		lines = append(lines, a.styles.Error.Render(truncatePlain("handoff history unavailable: "+history.err.Error(), width)))
	default:
		for _, handoff := range history.handoffs {
			if len(lines) >= budget {
				break
			}
			lines = append(lines, a.styles.Muted.Render(truncatePlain(handoffHistoryLine(a.now(), handoff), width)))
		}
	}
	return lines
}

// hasWatchSection reports whether the compact WATCH section has anything to
// say for the selected pull request.
func (a *App) hasWatchSection(pr model.PullRequest) bool {
	_, armed := a.engine.Status(pr.Key())
	history := a.handoffHistory[pr.Key()]
	return armed ||
		len(a.activity[pr.Key()]) > 0 ||
		history.loading ||
		history.err != nil ||
		len(history.handoffs) > 0
}

// renderWatchPage draws all watcher information already held by the app.
func (a *App) renderWatchPage(pr model.PullRequest, width, height int) []string {
	lines := a.watchPageLines(pr, width)
	if len(lines) == 0 {
		return a.centeredNotice("nothing to show for WATCH", width, a.styles.Meta)
	}

	window := max(height-1, 1)
	start := min(a.watchOffset, max(len(lines)-1, 0))
	end := min(start+window, len(lines))
	out := append([]string(nil), lines[start:end]...)
	if end < len(lines) || start > 0 {
		out = append(out, a.styles.Muted.Render(fmt.Sprintf("  %d–%d of %d",
			start+1, end, len(lines))))
	}
	return out
}

// watchPageLines returns the expanded WATCH page before scrolling is applied.
func (a *App) watchPageLines(pr model.PullRequest, width int) []string {
	if !a.hasWatchSection(pr) {
		return nil
	}

	line := func(style lipgloss.Style, text string) string {
		return style.Render(truncatePlain(text, width))
	}

	status, armed := a.engine.Status(pr.Key())
	events := a.activity[pr.Key()]
	history := a.handoffHistory[pr.Key()]
	lines := []string{
		a.sectionHeading("WATCH", false),
		line(a.styles.Repo, pr.Key().String()),
		"",
	}

	if !armed {
		lines = append(lines, line(a.styles.Meta, "state: not watching"))
	} else {
		lines = append(lines, line(a.styles.Watch, "state: "+status.Tier.String()))
		lines = append(lines, line(a.styles.Meta,
			"cadence: every "+model.HumanDuration(status.Interval)))
		if status.NextDue.After(a.now()) {
			lines = append(lines, line(a.styles.Meta,
				"next check: in "+model.HumanDuration(status.NextDue.Sub(a.now()))))
		} else {
			lines = append(lines, line(a.styles.Meta, "next check: due now"))
		}
		if status.SnapshotSeen {
			lines = append(lines, line(a.styles.Meta, "snapshot: seen"))
		} else {
			lines = append(lines, line(a.styles.Meta, "snapshot: waiting for first check"))
		}
		if status.PollsUntilPrecise > 0 {
			lines = append(lines, line(a.styles.Meta,
				fmt.Sprintf("precise review: in %d polls", status.PollsUntilPrecise)))
		}
	}

	if current := a.watching[pr.Key()]; current != "" {
		lines = append(lines, line(a.styles.Status, "operation: "+current))
	} else if open, ok := a.feedback[pr.Key()]; ok {
		lines = append(lines, line(a.styles.Meta,
			fmt.Sprintf("feedback: %d open %s", open, plural(open, "thread"))))
	}

	lines = append(lines, "", a.styles.SectionHdr.Render("ACTIVITY"))
	if len(events) == 0 {
		lines = append(lines, line(a.styles.Meta, "none this session"))
	} else {
		for i := len(events) - 1; i >= 0; i-- {
			event := events[i]
			lines = append(lines, line(a.styles.Muted,
				watchAge(a.now(), event.at)+" · "+event.text))
		}
	}

	lines = append(lines, "", a.styles.SectionHdr.Render("HANDOFFS"))
	switch {
	case history.loading:
		lines = append(lines, line(a.styles.Meta, "loading handoff history…"))
	case history.err != nil:
		lines = append(lines, line(a.styles.Error,
			"handoff history unavailable: "+history.err.Error()))
	case len(history.handoffs) == 0:
		lines = append(lines, line(a.styles.Meta, "none recorded"))
	default:
		for _, handoff := range history.handoffs {
			lines = append(lines, line(a.styles.Muted, handoffHistoryLine(a.now(), handoff)))
			if handoff.Workspace != "" {
				lines = append(lines, line(a.styles.Muted, "  workspace: "+handoff.Workspace))
			}
			if handoff.Tab != "" {
				lines = append(lines, line(a.styles.Muted, "  tab: "+handoff.Tab))
			}
		}
	}
	return lines
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

func watchAge(now, at time.Time) string {
	age := model.HumanAge(now.Sub(at))
	if age == "just now" {
		return age
	}
	return age + " ago"
}
