package ui

import (
	"fmt"

	"github.com/relloyd/prutil/internal/model"
)

// renderList draws the pull request list pane.
func (a *App) renderList(width, height int) []string {
	state := a.cur()
	switch {
	case state.err != nil && len(state.prs) == 0:
		return a.centeredNotice("could not reach GitHub: "+state.err.Error(), width, a.styles.Error)
	case state.loading && len(state.prs) == 0:
		return a.centeredNotice(a.spin.View()+" loading your "+a.active.String()+" pull requests…", width, a.styles.Meta)
	case len(state.prs) == 0:
		return a.centeredNotice("no "+a.active.String()+" pull requests. press r to refresh.", width, a.styles.Meta)
	}

	// One line is held back for the position indicator, so it is never clipped.
	rows := max((height-1)/rowHeight, 1)
	start := min(state.listOffset, max(len(state.prs)-1, 0))
	end := min(start+rows, len(state.prs))

	lines := make([]string, 0, height)
	for i := start; i < end; i++ {
		lines = append(lines, a.renderRow(state.prs[i], width, i == state.cursor)...)
	}
	if end < len(state.prs) || start > 0 {
		lines = append(lines, a.styles.Muted.Render(fmt.Sprintf("  %d–%d of %d", start+1, end, len(state.prs))))
	}
	return lines
}

// renderRow draws one pull request as a fixed-height block, so that scrolling
// arithmetic stays exact whatever the terminal width.
func (a *App) renderRow(pr model.PullRequest, width int, selected bool) []string {
	inner := max(width-2, 1)

	prefix := "  "
	titleStyle := a.styles.Text
	if selected {
		prefix = a.styles.SelectBar.Render("▌") + " "
		titleStyle = a.styles.Title
	}

	age := a.styles.Meta.Render(a.ageText(pr))
	identity := fitSegs(max(inner-lenOf(age)-1, 1), " ",
		a.styles.dot(a.rollupFor(pr)),
		seg{text: "#" + fmt.Sprint(pr.Number), style: a.styles.Number},
		seg{text: pr.Repo, style: a.styles.Repo},
	)

	title := wrapLines(pr.Title, inner, 2)
	for len(title) < 2 {
		title = append(title, "")
	}

	branchWidth := max(inner/2-2, 8)
	branchSegs := []seg{
		{text: truncatePlain(pr.HeadRef, branchWidth), style: a.styles.Branch},
		{text: "→", style: a.styles.Arrow},
		{text: truncatePlain(pr.BaseRef, branchWidth), style: a.styles.Branch},
	}
	branchSegs = append(branchSegs, a.styles.badges(pr)...)

	metaSegs := []seg{{text: a.checksSummary(pr), style: a.styles.Meta}}
	if review := a.styles.reviewBadge(pr); review.text != "" {
		metaSegs = append(metaSegs, review)
	}
	if diff := diffText(pr); diff != "" {
		metaSegs = append(metaSegs, seg{text: diff, style: a.styles.Meta})
	}
	// A closed pull request is already dated by its close time on the right of
	// the row, and its last update is almost always that same moment, so
	// repeating it here would be noise.
	if pr.ClosedAt.IsZero() && !pr.UpdatedAt.IsZero() {
		metaSegs = append(metaSegs, seg{
			text:  "upd " + model.HumanAge(a.now().Sub(pr.UpdatedAt)),
			style: a.styles.Meta,
		})
	}

	return []string{
		prefix + justify(inner, identity, age),
		prefix + titleStyle.Render(title[0]),
		prefix + titleStyle.Render(title[1]),
		prefix + fitSegs(inner, " ", branchSegs...),
		prefix + fitSegs(inner, "  ", metaSegs...),
		"",
	}
}

// ageText is the timestamp on the right of a list row: how long an open pull
// request has been waiting, or how long ago a closed one was closed.
func (a *App) ageText(pr model.PullRequest) string {
	if pr.ClosedAt.IsZero() {
		return model.HumanAge(a.now().Sub(pr.CreatedAt)) + " old"
	}
	return model.HumanAge(a.now().Sub(pr.ClosedAt)) + " ago"
}

// checksSummary describes the state of a pull request's checks for a list row,
// falling back to the rollup while the detail is still loading.
func (a *App) checksSummary(pr model.PullRequest) string {
	state, ok := a.checks[pr.Key()]
	switch {
	case !ok || state.loading:
		return "checks…"
	case state.err != nil:
		return "checks unavailable"
	default:
		return countsText(model.CountChecks(state.checks))
	}
}

// rollupFor prefers the freshly counted checks over the rollup that came with
// the list, so the dot and the counts never disagree.
func (a *App) rollupFor(pr model.PullRequest) model.Status {
	if state, ok := a.checks[pr.Key()]; ok && state.loaded && state.err == nil && len(state.checks) > 0 {
		return model.CountChecks(state.checks).Rollup()
	}
	return pr.Rollup
}

// truncatePlain shortens unstyled text to width columns.
func truncatePlain(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if lenOf(s) <= width {
		return s
	}
	return shorten(s, width)
}
