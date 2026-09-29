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

	rows := listRows(height)
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
// arithmetic stays exact whatever the terminal width. There is no blank
// separator line between one row and the next: the leading dot on the
// identity line is what marks where a row begins.
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
		a.watchSeg(pr),
		a.adoptedSeg(pr),
		seg{text: "#" + fmt.Sprint(pr.Number), style: a.styles.Number},
		seg{text: pr.Repo, style: a.styles.Repo},
	)

	title := wrapLines(pr.Title, inner, 2)
	for len(title) < 2 {
		title = append(title, "")
	}

	// A one-line title leaves its second line spare. Rather than let it sit
	// blank, the diff stat moves up into it, right-aligned, and drops out of
	// the meta line below; a title that needed both lines leaves the diff
	// stat where it has always been. Either way it is shown exactly once.
	diff := a.styles.diffSeg(pr)
	titleLine2 := prefix + titleStyle.Render(title[1])
	diffOnTitleLine := title[1] == "" && diff.text != ""
	if diffOnTitleLine {
		titleLine2 = prefix + justify(inner, "", diff.style.Render(diff.text))
	}

	branchWidth := max(inner/2-2, 8)
	branchSegs := []seg{
		{text: truncatePlain(pr.HeadRef, branchWidth), style: a.styles.Branch},
		{text: "→", style: a.styles.Arrow},
		{text: truncatePlain(pr.BaseRef, branchWidth), style: a.styles.Branch},
	}
	branchSegs = append(branchSegs, a.styles.badges(pr)...)
	branchSegs = append(branchSegs, a.authorSeg(pr))

	metaSegs := []seg{a.checkSeg(pr)}
	if review := a.styles.reviewBadge(pr); review.text != "" {
		metaSegs = append(metaSegs, review)
	}
	if open := a.feedbackSeg(pr); open.text != "" {
		metaSegs = append(metaSegs, open)
	}
	if !diffOnTitleLine && diff.text != "" {
		metaSegs = append(metaSegs, diff)
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
		titleLine2,
		prefix + fitSegs(inner, " ", branchSegs...),
		prefix + fitSegs(inner, "  ", metaSegs...),
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

// checkSeg describes the state of a pull request's checks for a list row,
// falling back to the rollup while the detail is still loading. Once the
// counts are in, each one carries its own status colour rather than one flat
// summary string.
func (a *App) checkSeg(pr model.PullRequest) seg {
	state, ok := a.checks[pr.Key()]
	switch {
	case !ok || state.loading:
		return seg{text: "checks…", style: a.styles.Meta}
	case state.err != nil:
		return seg{text: "checks unavailable", style: a.styles.Meta}
	default:
		return a.styles.checkCountSeg(model.CountChecks(state.checks))
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
