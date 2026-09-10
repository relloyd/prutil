package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/relloyd/prutil/internal/home"
	"github.com/relloyd/prutil/internal/model"
	"github.com/relloyd/prutil/internal/watch"
)

// watchDetail renders the selected pull request's current watcher state and
// recent in-session activity, while leaving room for the checks below it.
func (a *App) watchDetail(pr model.PullRequest, width, budget int) []string {
	status, armed := a.engine.Status(pr.Key())
	events := a.activity[pr.Key()]
	history := a.handoffHistory[pr.Key()]
	hasHistory := history.loading || history.err != nil || len(history.handoffs) > 0
	if budget < 2 || (!armed && len(events) == 0 && !hasHistory) {
		return nil
	}

	lines := []string{a.styles.SectionHdr.Render("WATCH")}
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
		text := model.HumanAge(a.now().Sub(event.at)) + " ago · " + event.text
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

// handoffHistoryLine keeps one durable handoff record useful in a narrow pane.
func handoffHistoryLine(now time.Time, handoff home.Handoff) string {
	parts := []string{model.HumanAge(now.Sub(handoff.At)) + " ago", handoff.Outcome}
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
