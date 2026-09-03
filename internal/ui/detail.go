package ui

import (
	"fmt"

	"github.com/relloyd/prutil/internal/model"
)

// renderDetail draws the pane describing the selected pull request and its
// checks.
func (a *App) renderDetail(width, height int) []string {
	pr, ok := a.selectedPR()
	if !ok {
		if a.loading {
			return a.centeredNotice("", width, a.styles.Meta)
		}
		return a.centeredNotice("nothing selected", width, a.styles.Meta)
	}

	lines := a.detailHeader(pr, width)
	state := a.checks[pr.Key()]

	switch {
	case state.err != nil:
		lines = append(lines, a.styles.Error.Render("checks unavailable"))
		lines = append(lines, wrapLines(state.err.Error(), width, 3)...)
		return lines
	case !state.loaded:
		lines = append(lines, a.styles.Meta.Render(a.spin.View()+" loading checks…"))
		return lines
	case len(state.checks) == 0:
		lines = append(lines, a.styles.Meta.Render("no checks on the head commit"))
		return lines
	}

	lines = append(lines, a.styles.SectionHdr.Render(fmt.Sprintf("CHECKS (%d)", len(state.checks))))

	window := checkWindow(height, len(lines))
	start := min(a.detailOffset, max(len(state.checks)-1, 0))
	end := min(start+window, len(state.checks))
	for i := start; i < end; i++ {
		selected := a.focus == paneDetail && i == a.detailCursor
		lines = append(lines, a.renderCheck(state.checks[i], width, selected))
	}
	if end < len(state.checks) || start > 0 {
		lines = append(lines, a.styles.Muted.Render(fmt.Sprintf("  %d–%d of %d", start+1, end, len(state.checks))))
	}
	return lines
}

// detailHeader renders everything above the check list.
func (a *App) detailHeader(pr model.PullRequest, width int) []string {
	lines := []string{
		fitSegs(width, " ",
			seg{text: pr.Repo, style: a.styles.Repo},
			seg{text: "#" + fmt.Sprint(pr.Number), style: a.styles.Number},
		),
	}
	for _, line := range wrapLines(pr.Title, width, 3) {
		lines = append(lines, a.styles.Title.Render(line))
	}
	lines = append(lines, "")

	timings := []seg{{text: "opened " + model.HumanAge(a.now().Sub(pr.CreatedAt)) + " ago", style: a.styles.Meta}}
	if !pr.UpdatedAt.IsZero() {
		timings = append(timings, seg{
			text:  "updated " + model.HumanAge(a.now().Sub(pr.UpdatedAt)) + " ago",
			style: a.styles.Meta,
		})
	}
	lines = append(lines, fitSegs(width, " · ", timings...))

	lines = append(lines, fitSegs(width, " ",
		seg{text: truncatePlain(pr.HeadRef, max(width/2-2, 8)), style: a.styles.Branch},
		seg{text: "→", style: a.styles.Arrow},
		seg{text: truncatePlain(pr.BaseRef, max(width/2-2, 8)), style: a.styles.Branch},
	))

	size := []seg{}
	if diff := diffText(pr); diff != "" {
		size = append(size, seg{text: diff, style: a.styles.Meta})
	}
	if pr.Comments > 0 {
		size = append(size, seg{text: fmt.Sprintf("%d comments", pr.Comments), style: a.styles.Meta})
	}
	if len(size) > 0 {
		lines = append(lines, fitSegs(width, " · ", size...))
	}

	badges := a.styles.badges(pr)
	if review := a.styles.reviewBadge(pr); review.text != "" {
		badges = append([]seg{review}, badges...)
	}
	if len(badges) > 0 {
		lines = append(lines, fitSegs(width, "  ", badges...))
	}

	return append(lines, "")
}

// renderCheck draws one check run as a single line: state, name, and how long
// it took, with the duration pushed to the right-hand edge.
func (a *App) renderCheck(check model.Check, width int, selected bool) string {
	prefix := "  "
	if selected {
		prefix = a.styles.SelectBar.Render("▌") + " "
	}
	inner := max(width-2, 1)

	right := ""
	if d := model.HumanDuration(check.Duration(a.now())); d != "" {
		right = a.styles.Muted.Render(d)
	}

	name := check.Name
	if name == "" {
		name = "(unnamed check)"
	}
	segs := []seg{
		{text: statusGlyph(check.Status), style: a.styles.statusStyle(check.Status)},
		{text: name, style: a.styles.Text},
	}
	if check.Workflow != "" && check.Workflow != name {
		segs = append(segs, seg{text: "· " + check.Workflow, style: a.styles.Muted})
	}

	left := fitSegs(max(inner-lenOf(right)-1, 1), " ", segs...)
	return prefix + justify(inner, left, right)
}

// checksHeight is the number of lines the check list may occupy, used by the
// scroll clamp before anything has been rendered.
func (a *App) checksHeight() int {
	pr, ok := a.selectedPR()
	if !ok {
		return 1
	}
	_, detailWidth := a.paneWidths()
	// One line is spent on the CHECKS heading.
	return checkWindow(a.bodyHeight(), len(a.detailHeader(pr, detailWidth))+1)
}

// checkWindow is the number of check lines that fit beneath a header of the
// given height, holding one line back for the position indicator.
func checkWindow(height, headerLines int) int {
	return max(height-headerLines-1, 1)
}
