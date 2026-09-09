package ui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	tea "charm.land/bubbletea/v2"
)

const (
	// headerHeight is the title line plus the rule beneath it.
	headerHeight = 2
	// paneGap is the vertical border plus the padding column that separates the
	// two panes.
	paneGap = 2
	// minDetailWidth keeps the check names readable when the terminal is only
	// just wide enough for a split.
	minDetailWidth = 32
	// minListWidth is the narrowest useful list pane.
	minListWidth = 30
)

// View implements tea.Model.
func (a *App) View() tea.View {
	v := tea.NewView(a.render())
	v.AltScreen = true
	v.WindowTitle = "prutil"
	return v
}

// render draws the whole screen, always producing exactly height lines.
func (a *App) render() string {
	if a.width <= 0 || a.height <= 0 {
		return "starting prutil…"
	}

	lines := make([]string, 0, a.height)
	lines = append(lines, a.renderHeader()...)
	lines = append(lines, a.renderBody()...)
	lines = append(lines, a.footerLines()...)

	if len(lines) > a.height {
		lines = lines[:a.height]
	}
	return strings.Join(lines, "\n")
}

// renderHeader draws the title bar and the rule beneath it.
func (a *App) renderHeader() []string {
	left := a.styles.Header.Render("prutil")
	if a.version != "" {
		left += " " + a.styles.Muted.Render(a.version)
	}

	state := a.cur()
	meta := []string{}
	if state.unavailable > 0 {
		meta = append(meta, fmt.Sprintf("%d repos unreachable", state.unavailable))
	}
	if state.enriching {
		meta = append(meta, "loading more…")
	}
	if !state.lastRefresh.IsZero() {
		meta = append(meta, "updated "+state.lastRefresh.Format("15:04:05"))
	}

	left += "  "
	if a.narrow() {
		pane := "pull requests"
		if a.focus == paneDetail {
			pane = "checks"
		}
		left += a.styles.Meta.Render(pane + " · ")
	}
	// The mode is the one part of the header the reader looks for at a glance,
	// so it is styled apart from the grey the rest of the line uses.
	left += a.styles.Mode.Render(a.modeText())
	if a.autoLeft > 0 {
		// Auto-refresh is a mode with a countdown, and the whole point of it is
		// to be left running while the reader watches; the header is where they
		// find out how long it has left.
		left += a.styles.Auto.Render(fmt.Sprintf(" · auto-refresh ×%d", a.autoLeft))
	}
	if len(meta) > 0 {
		left += a.styles.Meta.Render(" · " + strings.Join(meta, " · "))
	}

	right := ""
	if a.busy() {
		right = a.spin.View()
	}

	return []string{
		justify(a.width, left, right),
		a.styles.PaneBorder.Render(strings.Repeat("─", a.width)),
	}
}

// modeText names the view on screen and how much it holds. It is the header's
// answer to "am I looking at open or closed pull requests?".
func (a *App) modeText() string {
	state := a.cur()
	switch {
	case state.loading && len(state.prs) == 0:
		return "loading " + a.active.String() + " PRs"
	case len(state.prs) == 1:
		return "1 " + a.active.String() + " PR"
	default:
		return fmt.Sprintf("%d %s PRs", len(state.prs), a.active)
	}
}

// renderBody draws the list and, when there is room, the detail pane beside it.
func (a *App) renderBody() []string {
	height := a.bodyHeight()
	listWidth, detailWidth := a.paneWidths()

	if a.narrow() {
		if a.focus == paneDetail {
			return clipLines(a.renderDetail(detailWidth, height), height, detailWidth)
		}
		return clipLines(a.renderList(listWidth, height), height, listWidth)
	}

	left := clipLines(a.renderList(listWidth, height), height, listWidth)
	right := clipLines(a.renderDetail(detailWidth, height), height, detailWidth)
	border := a.styles.PaneBorder.Render("│")

	out := make([]string, height)
	for i := range out {
		out[i] = left[i] + " " + border + right[i]
	}
	return out
}

// footerLines draws the transient status line and the key help.
func (a *App) footerLines() []string {
	notice := ""
	switch err := a.cur().err; {
	case err != nil:
		notice = a.styles.Error.Render("error: " + ansi.Truncate(err.Error(), max(a.width-7, 10), ellipsis))
	case a.status != "":
		notice = a.styles.Status.Render(ansi.Truncate(a.status, max(a.width, 10), ellipsis))
	}

	lines := []string{notice}
	for _, line := range strings.Split(a.help.View(a.keys), "\n") {
		// The help component drops bindings that do not fit, but it gives up at
		// some widths, so the line is clipped here as well.
		lines = append(lines, ansi.Truncate(line, a.width, ellipsis))
	}
	return lines
}

// bodyHeight is the number of lines available to the panes.
func (a *App) bodyHeight() int {
	return max(a.height-headerHeight-len(a.footerLines()), 1)
}

// paneWidths splits the terminal between the list and the detail pane. In
// narrow mode both panes are the full width, because only one is drawn.
func (a *App) paneWidths() (listWidth, detailWidth int) {
	if a.narrow() {
		return a.width, a.width
	}

	listWidth = max(a.width*42/100, minListWidth)
	detailWidth = a.width - listWidth - paneGap
	if detailWidth < minDetailWidth {
		detailWidth = minDetailWidth
		listWidth = a.width - detailWidth - paneGap
	}
	if listWidth < minListWidth {
		listWidth = minListWidth
		detailWidth = max(a.width-listWidth-paneGap, 1)
	}
	return listWidth, detailWidth
}

// centeredNotice renders a short message inside a pane.
func (a *App) centeredNotice(text string, width int, style lipgloss.Style) []string {
	out := []string{""}
	for _, line := range wrapLines(text, max(width-2, 1), 4) {
		out = append(out, " "+style.Render(line))
	}
	return out
}
