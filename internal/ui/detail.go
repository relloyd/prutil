package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/relloyd/prutil/internal/model"
)

// renderDetail draws the pane describing the selected pull request and its
// checks.
func (a *App) renderDetail(width, height int) []string {
	pr, ok := a.selectedPR()
	if !ok {
		if a.cur().loading {
			return a.centeredNotice("", width, a.styles.Meta)
		}
		return a.centeredNotice("nothing selected", width, a.styles.Meta)
	}

	if a.page == detailWatchPage {
		return a.renderWatchPage(pr, width, height)
	}

	lines := a.detailHeader(pr, width)
	// Keep a check heading and at least one check line visible below watcher
	// diagnostics, even in a short terminal.
	watchBudget := max(height-len(lines)-4, 0)
	focused := a.focus == paneDetail
	if watch := a.watchDetail(pr, width, watchBudget, focused && a.section == detailWatch); len(watch) > 0 {
		lines = append(lines, watch...)
		lines = append(lines, "")
	}
	state := a.checks[pr.Key()]

	switch {
	case state.err != nil && len(state.checks) == 0:
		lines = append(lines, sectionPad+a.styles.Error.Render("checks unavailable"))
		for _, line := range wrapLines(state.err.Error(), width-sectionIndent, 3) {
			lines = append(lines, sectionPad+line)
		}
		return lines
	case !state.loaded:
		lines = append(lines, sectionPad+a.styles.Meta.Render(a.spin.View()+" loading checks…"))
		return lines
	case len(state.checks) == 0:
		lines = append(lines, sectionPad+a.styles.Meta.Render("no checks on the head commit"))
		return lines
	}

	lines = append(lines, a.partialCheckLines(state, width)...)
	// The chip goes on the check that is selected rather than on its heading
	// as well, so there is one mark for the one thing selected. The heading
	// brightens to say which section the cursor is in.
	checksHeading := fmt.Sprintf("CHECKS (%d)", len(state.checks))
	if focused && a.section == detailChecks {
		lines = append(lines, sectionPad+a.styles.Title.Render(checksHeading))
	} else {
		lines = append(lines, a.sectionHeading(checksHeading, false))
	}

	window := checkWindow(height, len(lines))
	start := min(a.detailOffset, max(len(state.checks)-1, 0))
	end := min(start+window, len(state.checks))
	for i := start; i < end; i++ {
		selected := a.focus == paneDetail && a.section == detailChecks && i == a.detailCursor
		lines = append(lines, a.renderCheck(state.checks[i], width, selected))
	}
	if end < len(state.checks) || start > 0 {
		lines = append(lines, a.styles.Muted.Render(fmt.Sprintf("  %d–%d of %d", start+1, end, len(state.checks))))
	}
	return lines
}

// sectionIndent is how far everything under the pull request's header sits in
// from the pane's edge: the section headings, and every line beneath them. The
// headings need the room for the selection bar, and what they head lines up
// with them rather than sitting outdented beneath.
const sectionIndent = 2

// sectionPad is sectionIndent as the spaces it takes.
var sectionPad = strings.Repeat(" ", sectionIndent)

// sectionHeading renders one of the detail sections. selected puts the chip
// on it, which is only ever right while the detail pane has the focus: a chip
// drawn before then leaves → with nothing visible to change.
func (a *App) sectionHeading(text string, selected bool) string {
	prefix := sectionPad
	style := a.styles.SectionHdr
	if selected {
		prefix = a.styles.SelectBar.Render("▌") + " "
		style = a.styles.Title
	}
	return prefix + style.Render(text)
}

// drillHint is what follows a section heading with a page of its own behind
// it: the chevron always, so the page can be seen to be there at a glance, and
// where → goes once the heading is selected. CHECKS has no page, and no
// chevron, which is part of what makes WATCH's mean something.
const (
	drillHint         = " ›"
	drillHintSelected = " › watch log"
)

// drillHeading is sectionHeading for a section that → drills into. The hint
// gives way to the heading in a pane too narrow for both, the longer one first.
func (a *App) drillHeading(text string, selected bool, width int) string {
	used := sectionIndent + lenOf(text)
	hint := drillHint
	if selected && used+lenOf(drillHintSelected) <= width {
		hint = drillHintSelected
	}
	if used+lenOf(hint) > width {
		hint = ""
	}
	return a.sectionHeading(text, selected) + a.styles.Muted.Render(hint)
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
	switch {
	// For a closed pull request the close is the useful second date. Its last
	// update is nearly always that same moment, so showing both would waste the
	// line.
	case !pr.ClosedAt.IsZero():
		verb := "closed "
		if !pr.MergedAt.IsZero() {
			verb = "merged "
		}
		timings = append(timings, seg{
			text:  verb + model.HumanAge(a.now().Sub(pr.ClosedAt)) + " ago",
			style: a.styles.Meta,
		})
	case !pr.UpdatedAt.IsZero():
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
		size = append(size, seg{text: fmt.Sprintf("%d %s", pr.Comments, plural(pr.Comments, "conversation")), style: a.styles.Meta})
	}
	if pr.ReviewThreadCount != nil {
		count := *pr.ReviewThreadCount
		size = append(size, seg{text: fmt.Sprintf("%d review %s", count, plural(count, "thread")), style: a.styles.Meta})
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

	// The list row's adopted marker and author are among the first things it
	// drops when narrow, and adoption changes what w and a handoff do, so the
	// detail pane says it on a line of its own.
	if adopted := a.adoptedSeg(pr); adopted.text != "" {
		lines = append(lines, fitSegs(width, " · ",
			seg{text: adopted.text + " adopted", style: adopted.style},
			a.authorSeg(pr),
		))
	}

	return append(lines, "")
}

// renderCheck draws one check run as a single line: state, name, and how long
// it took, with the duration pushed to the right-hand edge.
func (a *App) renderCheck(check model.Check, width int, selected bool) string {
	prefix := sectionPad
	if selected {
		prefix = a.styles.SelectBar.Render("▌") + " "
	}
	inner := max(width-sectionIndent, 1)

	right := ""
	if d := model.HumanDuration(check.Duration(a.now())); d != "" {
		right = a.styles.Muted.Render(d)
	}

	name := check.Name
	if name == "" {
		name = "(unnamed check)"
	}
	budget := inner
	if right != "" {
		budget = inner - lenOf(right) - 1
		if budget-2 < min(lenOf(name), 4) {
			right = ""
			budget = inner
		}
	}
	nameWidth := budget - 2
	if nameWidth == 1 {
		name = ansi.Truncate(name, 1, "")
	} else {
		name = truncatePlain(name, nameWidth)
	}
	segs := []seg{
		{text: statusGlyph(check.Status), style: a.styles.statusStyle(check.Status)},
		{text: name, style: a.styles.Text},
	}
	if check.Workflow != "" && check.Workflow != name {
		segs = append(segs, seg{text: "· " + check.Workflow, style: a.styles.Muted})
	}

	left := fitSegs(budget, " ", segs...)
	return prefix + justify(inner, left, right)
}

// partialCheckLines warns that the checks on screen are not all of them. It is
// a function of its own because checksHeight has to count it too, or the
// scroll clamp works to a taller window than the one drawn.
func (a *App) partialCheckLines(state checkState, width int) []string {
	if state.err == nil || len(state.checks) == 0 {
		return nil
	}
	lines := []string{sectionPad + a.styles.Error.Render("partial checks · automatic check actions paused")}
	for _, line := range wrapLines(state.err.Error(), width-sectionIndent, 2) {
		lines = append(lines, sectionPad+line)
	}
	return lines
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
	header := a.detailHeader(pr, detailWidth)
	watchBudget := max(a.bodyHeight()-len(header)-4, 0)
	watch := a.watchDetail(pr, detailWidth, watchBudget, false)
	gap := 0
	if len(watch) > 0 {
		gap = 1
	}
	partial := a.partialCheckLines(a.checks[pr.Key()], detailWidth)
	return checkWindow(a.bodyHeight(), len(header)+len(watch)+gap+len(partial)+1)
}

// checkWindow is the number of check lines that fit beneath a header of the
// given height, holding one line back for the position indicator.
func checkWindow(height, headerLines int) int {
	return max(height-headerLines-1, 1)
}
