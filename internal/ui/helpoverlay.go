package ui

import (
	"fmt"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/sahilm/fuzzy"
)

const (
	// overlayMaxWidth keeps the overlay readable on a very wide terminal
	// rather than stretching a short line across all of it.
	overlayMaxWidth = 88
	// overlayFullWidth and overlayFullHeight are the sizes below which the
	// overlay stops floating and takes the whole terminal, because margins
	// there cost more room than they are worth.
	overlayFullWidth  = 50
	overlayFullHeight = 16
	// overlayKeyWidth caps the key column, so one long alias cannot squeeze
	// every title.
	overlayKeyWidth = 12
	// overlayDetailLines is how much of the selected shortcut's explanation
	// is shown beneath the list.
	overlayDetailLines = 2
	// overlayMinRows is the fewest list rows worth keeping the detail strip
	// for. Below it the strip goes, so the list keeps the room.
	overlayMinRows = 3
	// overlayChrome is the lines every overlay spends around its list: the
	// top edge, the filter, the rule beneath it, the blank line that sets the
	// last row off from the bottom edge, and that edge itself. Without the
	// blank one the text runs straight into the border, which carries its own
	// hints, and the two read as one line.
	overlayChrome = 5
)

// helpOverlay is the ? shortcut list. It holds what the filter found and where
// the reader is in it. The entries themselves are asked of
// keyMap.helpSections whenever the query changes, so it never holds a stale
// copy of the bindings.
type helpOverlay struct {
	open  bool
	keys  overlayKeyMap
	input textinput.Model
	// matches are the entries the query kept, best first. rows is what the
	// list draws: the matches, with a heading before each section while
	// nothing is filtered. rowOf maps a match to its row.
	matches []helpMatch
	rows    []helpRow
	rowOf   []int
	// total is how many entries there are unfiltered, fullRows how many rows
	// they draw, and keyWidth how wide their key column is. The box is sized
	// from the unfiltered list so that it does not jump about as the reader
	// types.
	total    int
	fullRows int
	keyWidth int
	filtered bool
	cursor   int
	offset   int
}

// helpMatch is one entry the filter kept.
type helpMatch struct {
	entry   helpEntry
	section string
	// hits are the byte offsets of the title characters the query matched.
	hits []int
}

// helpRow is one line of the overlay's list: a section heading, or the match
// at an index.
type helpRow struct {
	heading string
	match   int
}

// helpLayout is where the overlay sits and how its height is spent.
type helpLayout struct {
	x, y          int
	width, height int
	// inner is the width inside the frame and its padding.
	inner int
	// window is how many list rows are drawn, and detail whether the selected
	// shortcut's explanation fits beneath them.
	window int
	detail bool
}

// openHelp shows the overlay with an empty filter that already has the
// keyboard, so the first key typed filters.
func (a *App) openHelp() tea.Cmd {
	a.overlay = helpOverlay{open: true, keys: defaultOverlayKeys(), input: textinput.New()}
	o := &a.overlay
	o.input.Prompt = "› "
	o.input.Placeholder = "type to filter…"
	o.input.SetStyles(a.styles.helpInput())

	a.refilter()
	o.total, o.fullRows = len(o.matches), len(o.rows)
	for _, m := range o.matches {
		o.keyWidth = max(o.keyWidth, lenOf(m.entry.label()))
	}
	o.keyWidth = min(o.keyWidth, overlayKeyWidth)
	a.resizeHelp()
	return o.input.Focus()
}

// closeHelp puts the overlay away. Nothing about it is kept, so the next ?
// starts from an empty filter at the top of the list.
func (a *App) closeHelp() {
	a.overlay = helpOverlay{}
}

// resizeHelp fits the filter and the scroll position to a new terminal size.
func (a *App) resizeHelp() {
	if !a.overlay.open {
		return
	}
	a.overlay.input.SetWidth(max(a.helpLayout().inner-lenOf(a.overlay.input.Prompt)-1, 1))
	a.clampHelpScroll()
}

// restyleHelp repaints the filter once the terminal has said whether it is
// light or dark.
func (a *App) restyleHelp() {
	if a.overlay.open {
		a.overlay.input.SetStyles(a.styles.helpInput())
	}
}

// updateHelpOverlay handles the messages the overlay takes for itself while it
// is open, and reports whether it took this one. Keys, pastes and the mouse
// belong to it; everything else, replies from GitHub included, goes on to the
// app as usual.
func (a *App) updateHelpOverlay(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return a.handleHelpKey(msg), true
	case tea.PasteMsg:
		return a.editHelpQuery(msg), true
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			a.moveHelp(-wheelStep)
		case tea.MouseWheelDown:
			a.moveHelp(wheelStep)
		}
		return nil, true
	case tea.MouseClickMsg:
		// A click lands on the overlay, not on the list drawn behind it.
		return nil, true
	}
	return nil, false
}

// handleHelpKey applies a key press to the open overlay.
func (a *App) handleHelpKey(msg tea.KeyPressMsg) tea.Cmd {
	keys := a.overlay.keys
	switch {
	case key.Matches(msg, keys.Quit):
		return tea.Quit
	case key.Matches(msg, keys.Close):
		a.closeHelp()
		return nil
	case key.Matches(msg, keys.Run):
		return a.runHelpSelection()
	case key.Matches(msg, keys.Up):
		a.moveHelp(-1)
		return nil
	case key.Matches(msg, keys.Down):
		a.moveHelp(1)
		return nil
	case key.Matches(msg, keys.PageUp):
		a.moveHelp(-a.helpLayout().window)
		return nil
	case key.Matches(msg, keys.PageDown):
		a.moveHelp(a.helpLayout().window)
		return nil
	case key.Matches(msg, keys.HalfPageUp):
		a.moveHelp(-max(a.helpLayout().window/2, 1))
		return nil
	case key.Matches(msg, keys.HalfPageDown):
		a.moveHelp(max(a.helpLayout().window/2, 1))
		return nil
	}
	return a.editHelpQuery(msg)
}

// runHelpSelection closes the overlay and presses the highlighted shortcut's
// key on the reader's behalf. Going back through handleKey is the point: the
// shortcut does exactly what pressing it would, including whatever it does
// differently depending on which pane has focus.
func (a *App) runHelpSelection() tea.Cmd {
	o := &a.overlay
	if len(o.matches) == 0 {
		return nil
	}
	entry := o.matches[o.cursor].entry
	a.closeHelp()
	if !entry.runnable() {
		return nil
	}
	press := keyPress(entry.binding.Keys()[0])
	if key.Matches(press, a.keys.Help) {
		return nil
	}
	_, cmd := a.handleKey(press)
	return cmd
}

// editHelpQuery hands a message to the filter and refilters when it changed
// what was typed.
func (a *App) editHelpQuery(msg tea.Msg) tea.Cmd {
	o := &a.overlay
	before := o.input.Value()
	var cmd tea.Cmd
	o.input, cmd = o.input.Update(msg)
	if o.input.Value() != before {
		a.refilter()
	}
	return cmd
}

// moveHelp steps the overlay's selection by delta, clamped to the list.
func (a *App) moveHelp(delta int) {
	o := &a.overlay
	if len(o.matches) == 0 {
		return
	}
	o.cursor = min(max(o.cursor+delta, 0), len(o.matches)-1)
	a.clampHelpScroll()
}

// clampHelpScroll keeps the selected row inside the drawn window.
func (a *App) clampHelpScroll() {
	o := &a.overlay
	if len(o.matches) == 0 {
		o.cursor, o.offset = 0, 0
		return
	}
	window := a.helpLayout().window
	row := o.rowOf[o.cursor]
	o.offset = clampOffset(o.offset, row, window, len(o.rows))
	// Scrolling up onto the first entry of a section brings its heading with
	// it, so the reader is never shown an entry without knowing where it is.
	if row > 0 && o.offset == row && o.rows[row-1].match < 0 && window > 1 {
		o.offset = row - 1
	}
}

// refilter rebuilds the list from the query and returns the selection to the
// top of it.
func (a *App) refilter() {
	o := &a.overlay
	query := o.input.Value()

	var all []helpMatch
	for _, section := range a.keys.helpSections(a.mouse) {
		for _, entry := range section.entries {
			all = append(all, helpMatch{entry: entry, section: section.title})
		}
	}

	o.filtered = query != ""
	o.matches = all
	if o.filtered {
		o.matches = rankHelp(query, all)
	}
	o.cursor, o.offset = 0, 0
	o.rows, o.rowOf = helpRows(o.matches, !o.filtered)
}

// rankHelp orders the entries a query matches, in three tiers. An entry bound
// to exactly the key typed comes first, case and all, so W finds the handoff
// rather than every sentence with a w in it. Entries whose title or keys
// fuzzy-match come next, best first. Last come entries whose explanation or
// section contains the query as typed, in section order.
//
// Only the short text is matched fuzzily, for two reasons found by typing into
// it. The fuzzy score charges a point for every character that did not match,
// so scored against a whole sentence a short entry that barely matches beat a
// long one whose title was exactly what was typed: "cop" found scroll before
// copy URL. And almost any few letters occur in order somewhere in a sentence:
// "agent" found refresh and go back.
func rankHelp(query string, all []helpMatch) []helpMatch {
	out := make([]helpMatch, 0, len(all))
	taken := make([]bool, len(all))
	names := make([]string, len(all))
	for i, m := range all {
		names[i] = m.entry.title + " " + m.entry.label()
		if slices.Contains(m.entry.binding.Keys(), query) {
			out = append(out, m)
			taken[i] = true
		}
	}

	found := fuzzy.FindNoSort(query, names)
	slices.SortStableFunc(found, func(x, y fuzzy.Match) int { return y.Score - x.Score })
	for _, f := range found {
		if taken[f.Index] {
			continue
		}
		taken[f.Index] = true
		m := all[f.Index]
		for _, i := range f.MatchedIndexes {
			if i < len(m.entry.title) {
				m.hits = append(m.hits, i)
			}
		}
		out = append(out, m)
	}

	lower := strings.ToLower(query)
	for i, m := range all {
		if !taken[i] && strings.Contains(strings.ToLower(m.entry.searchText(m.section)), lower) {
			out = append(out, m)
		}
	}
	return out
}

// helpRows lays the matches out as list rows, with a heading before each
// section when headings is set.
func helpRows(matches []helpMatch, headings bool) ([]helpRow, []int) {
	rows := make([]helpRow, 0, len(matches))
	rowOf := make([]int, len(matches))
	section := ""
	for i, m := range matches {
		if headings && m.section != section {
			rows = append(rows, helpRow{heading: m.section, match: -1})
			section = m.section
		}
		rowOf[i] = len(rows)
		rows = append(rows, helpRow{match: i})
	}
	return rows, rowOf
}

// helpLayout sizes and places the overlay for the current terminal.
func (a *App) helpLayout() helpLayout {
	l := helpLayout{width: a.width}
	room := a.height - overlayChrome
	if a.floating() {
		l.width = min(a.width-4, overlayMaxWidth)
		room -= 2
	}
	l.inner = max(l.width-4, 1)

	extra := 0
	if room-(overlayDetailLines+1) >= overlayMinRows {
		l.detail = true
		extra = overlayDetailLines + 1
	}
	l.window = max(min(room-extra, a.overlay.fullRows), 1)
	l.height = overlayChrome + extra + l.window
	l.x = max((a.width-l.width)/2, 0)
	l.y = max((a.height-l.height)/2, 0)
	return l
}

// renderHelpOverlay draws the overlay over a finished screen.
func (a *App) renderHelpOverlay(base []string) []string {
	l := a.helpLayout()
	return a.floatOver(base, a.helpBox(l), l.x, l.y, l.width)
}

// helpBox draws the overlay itself, frame and all, as l.height lines of
// l.width columns.
func (a *App) helpBox(l helpLayout) []string {
	o := &a.overlay
	row := func(content string) string { return a.frameRow(content, l.inner) }
	rule := a.frameRule(l.width)

	count := a.styles.Muted.Render(fmt.Sprintf("%d/%d", len(o.matches), o.total))
	box := make([]string, 0, l.height)
	box = append(box,
		a.edge(l.width, "╭", "╮", a.styles.OverlayTitle.Render("Keyboard shortcuts"), count),
		row(o.input.View()),
		rule,
	)

	list := a.helpListLines(l)
	for i := 0; i < l.window; i++ {
		line := ""
		if i < len(list) {
			line = list[i]
		}
		box = append(box, row(line))
	}

	if l.detail {
		detail := ""
		if len(o.matches) > 0 {
			detail = o.matches[o.cursor].entry.detail
		}
		lines := wrapLines(detail, l.inner, overlayDetailLines)
		box = append(box, rule)
		for i := 0; i < overlayDetailLines; i++ {
			text := ""
			if i < len(lines) {
				text = lines[i]
			}
			box = append(box, row(a.styles.Muted.Render(text)))
		}
	}
	box = append(box, row(""))
	return append(box, a.edge(l.width, "╰", "╯", a.helpHints(), ""))
}

// helpListLines draws the rows of the list that fall inside the window.
func (a *App) helpListLines(l helpLayout) []string {
	o := &a.overlay
	out := make([]string, 0, l.window)
	if len(o.matches) == 0 {
		out = append(out, a.styles.Muted.Render(fmt.Sprintf("no shortcut matches “%s”", o.input.Value())))
	}
	for r := o.offset; r < len(o.rows) && len(out) < l.window; r++ {
		row := o.rows[r]
		if row.match < 0 {
			out = append(out, "  "+a.styles.SectionHdr.Render(strings.ToUpper(row.heading)))
			continue
		}
		out = append(out, a.helpEntryLine(o.matches[row.match], row.match == o.cursor, l.inner))
	}
	return out
}

// helpEntryLine draws one shortcut: the selection bar, its keys, its title
// with the matched characters picked out, and, while filtering, the section
// it came from.
func (a *App) helpEntryLine(m helpMatch, selected bool, width int) string {
	prefix, titleStyle := "  ", a.styles.Text
	if selected {
		prefix, titleStyle = a.styles.SelectBar.Render("▌")+" ", a.styles.Title
	}

	keyWidth := a.overlay.keyWidth
	keys := a.styles.OverlayKey.Render(padTo(truncatePlain(m.entry.label(), keyWidth), keyWidth))
	room := max(width-2-keyWidth-2, 1)

	title := highlight(m.entry.title, m.hits, room, titleStyle, a.styles.MatchHit)
	tag := ""
	if a.overlay.filtered && lenOf(m.entry.title)+2+lenOf(m.section) <= room {
		tag = a.styles.Muted.Render(m.section)
	}
	return prefix + keys + "  " + justify(room, title, tag)
}

// helpHints names the overlay's own keys, for its bottom edge.
func (a *App) helpHints() string {
	k := a.overlay.keys
	pairs := []key.Help{
		{Key: k.Up.Help().Key + k.Down.Help().Key, Desc: "select"},
		k.Run.Help(),
		k.Close.Help(),
	}
	parts := make([]string, 0, len(pairs))
	for _, p := range pairs {
		parts = append(parts, a.styles.OverlayKey.Render(p.Key)+" "+a.styles.Muted.Render(p.Desc))
	}
	return strings.Join(parts, a.styles.Muted.Render(" · "))
}

// edge draws the top or bottom of the overlay's frame with text set into it,
// dropping the right-hand text and then shortening the left when the frame is
// too narrow for both. The text arrives styled; it is measured with lenOf.
func (a *App) edge(width int, open, close, left, right string) string {
	border := a.styles.OverlayBorder
	span := width - 4 // the two corners and the dash beside each
	cost := func(s string) int {
		if s == "" {
			return 0
		}
		return lenOf(s) + 2
	}

	if cost(left)+cost(right) >= span {
		right = ""
	}
	if left != "" && cost(left) >= span {
		if span-3 >= 1 {
			left = shorten(left, span-3)
		} else {
			left = ""
		}
	}
	fill := max(span-cost(left)-cost(right), 0)

	var b strings.Builder
	b.WriteString(border.Render(open + "─"))
	if left != "" {
		b.WriteString(" " + left + " ")
	}
	b.WriteString(border.Render(strings.Repeat("─", fill)))
	if right != "" {
		b.WriteString(" " + right + " ")
	}
	b.WriteString(border.Render("─" + close))
	return b.String()
}

// highlight renders plain text in base, with the characters at the given byte
// offsets in hit, cut to width columns.
func highlight(text string, hits []int, width int, base, hit lipgloss.Style) string {
	if width <= 0 {
		return ""
	}
	cut, tail := text, ""
	if lenOf(text) > width {
		cut, tail = ansi.Truncate(text, width-1, ""), base.Render(ellipsis)
	}

	marked := make(map[int]bool, len(hits))
	for _, i := range hits {
		marked[i] = true
	}

	var b strings.Builder
	start, on := 0, false
	flush := func(end int) {
		if end > start {
			style := base
			if on {
				style = hit
			}
			b.WriteString(style.Render(cut[start:end]))
		}
		start = end
	}
	for i := range cut {
		if marked[i] != on {
			flush(i)
			on = marked[i]
		}
	}
	flush(len(cut))
	return b.String() + tail
}
