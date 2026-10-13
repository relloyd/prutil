package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/relloyd/prutil/internal/home"
	"github.com/relloyd/prutil/internal/model"
)

// trayKind identifies the category of notification event for styling and glyph selection.
type trayKind int

const (
	// trayKindDesktop is a general desktop notification.
	trayKindDesktop trayKind = iota
	// trayKindApproved is recorded when all reviews on a pull request are approved.
	trayKindApproved
	// trayKindChecks is recorded when CI status checks pass.
	trayKindChecks
	// trayKindHandoff is recorded when an agent handoff is dispatched or updated.
	trayKindHandoff
	// trayKindHold is recorded when feedback is held for security or review.
	trayKindHold
	// trayKindWatch is recorded for watch-related state transitions.
	trayKindWatch
)

// maxTrayItems bounds in-memory notification history for the active session.
const maxTrayItems = 100

// trayRowHeight is the height in lines of each notification entry in the list.
const trayRowHeight = 3

// trayPaneHeader is the lines the sidebar draws above its first entry: the
// heading and the rule beneath it.
const trayPaneHeader = 2

// trayItem represents one recorded notification event during the session.
type trayItem struct {
	id  int
	at  time.Time
	key model.Key
	// url is the pull request's link as it was when the notification was
	// recorded, for when the pull request has since left every view. It comes
	// from GitHub, so it names the right host on an enterprise install.
	url      string
	title    string
	headline string
	detail   string
	kind     trayKind
	read     bool
}

// trayState holds the notification tray session history and navigation.
type trayState struct {
	keys      trayKeyMap
	nextID    int
	items     []trayItem
	cursor    int
	offset    int
	popupOpen bool
}

func (t *trayState) unreadCount() int {
	count := 0
	for _, it := range t.items {
		if !it.read {
			count++
		}
	}
	return count
}

func (t *trayState) markAllRead() {
	for i := range t.items {
		t.items[i].read = true
	}
}

func (t *trayState) record(item trayItem) {
	t.nextID++
	item.id = t.nextID
	t.items = append([]trayItem{item}, t.items...)
	if len(t.items) > maxTrayItems {
		t.items = t.items[:maxTrayItems]
	}
}

// recordTrayNotification records an event into the session's notification history,
// stripping any redundant PR key prefix from headline.
//
// Every text is folded onto one line as it is recorded. A title is the pull
// request author's text and a detail can be an error from another program, and
// a newline in either would make a card taller than trayRowHeight.
func (a *App) recordTrayNotification(key model.Key, title, headline, detail string, kind trayKind) {
	title, headline, detail = model.SafeLine(title), model.SafeLine(headline), model.SafeLine(detail)
	if key.Repo != "" || key.Number > 0 {
		prefix := key.String()
		if strings.HasPrefix(headline, prefix+": ") {
			headline = strings.TrimPrefix(headline, prefix+": ")
		} else if strings.HasPrefix(headline, prefix+" ") {
			headline = strings.TrimPrefix(headline, prefix+" ")
		}
	}

	pr, _ := a.findPR(key)
	a.tray.record(trayItem{
		at:       a.now(),
		key:      key,
		url:      pr.URL,
		title:    title,
		headline: headline,
		detail:   detail,
		kind:     kind,
		read:     false,
	})
}

// selectedTrayItem returns the notification item currently under cursor.
func (a *App) selectedTrayItem() (trayItem, bool) {
	if len(a.tray.items) == 0 || a.tray.cursor < 0 || a.tray.cursor >= len(a.tray.items) {
		return trayItem{}, false
	}
	return a.tray.items[a.tray.cursor], true
}

// toggleNotifications opens or closes the notification history popup, or shifts focus
// between the list and notifications sidebar when pinned.
func (a *App) toggleNotifications() {
	if a.sidebarVisible() {
		if a.focus == paneNotifications {
			a.focus = paneList
		} else {
			a.focus = paneNotifications
			a.tray.markAllRead()
			a.clampTrayScroll()
		}
		return
	}

	a.tray.popupOpen = !a.tray.popupOpen
	if a.tray.popupOpen {
		a.tray.markAllRead()
		a.clampTrayScroll()
	}
}

// toggleSidebarMode switches between the floating popup modal and a permanent
// sidebar pane. A save that fails changes nothing, and says why.
func (a *App) toggleSidebarMode() tea.Cmd {
	next := !a.homeCfg.Notifications.Sidebar
	path := []string{"notifications", "sidebar"}
	if err := a.saveSetting(path, strconv.FormatBool(next), func(c *home.Config) { c.Notifications.Sidebar = next }); err != nil {
		return status("could not save notification sidebar: " + err.Error())
	}
	if next {
		a.tray.popupOpen = false
		if a.sidebarVisible() {
			a.focus = paneNotifications
		}
	} else {
		if a.focus == paneNotifications {
			a.focus = paneList
		}
	}
	a.clampScroll()
	return nil
}

// openNotificationPR closes the notification popup and navigates to the selected notification's pull request.
func (a *App) openNotificationPR() tea.Cmd {
	item, ok := a.selectedTrayItem()
	if !ok || item.key == (model.Key{}) {
		return nil
	}
	a.tray.popupOpen = false
	return a.jumpToPR(item.key)
}

// jumpToPR moves the list cursor to the pull request matching key, switching
// views if necessary. The detail pane starts afresh, as it does whenever the
// list moves to another pull request.
func (a *App) jumpToPR(key model.Key) tea.Cmd {
	v, index, ok := a.locatePR(key)
	if !ok {
		a.status = fmt.Sprintf("PR %s not in current view", key.String())
		return nil
	}
	cmd := a.switchView(v)
	a.cur().cursor = index
	a.focus = paneList
	a.resetDetailNavigation()
	a.clampScroll()
	return tea.Batch(cmd, a.withSpinner(a.ensureChecks(key)))
}

// locatePR finds the pull request matching key, looking in the view on screen
// first so that one listed in both stays where the reader is.
func (a *App) locatePR(key model.Key) (view, int, bool) {
	for _, v := range append([]view{a.active}, allViews()...) {
		for i, pr := range a.views[v].prs {
			if pr.Key() == key {
				return v, i, true
			}
		}
	}
	return 0, 0, false
}

// findPR searches all views for the pull request matching key. prByKey is
// the open view alone, which is what the watcher wants and this does not.
func (a *App) findPR(key model.Key) (model.PullRequest, bool) {
	v, index, ok := a.locatePR(key)
	if !ok {
		return model.PullRequest{}, false
	}
	return a.views[v].prs[index], true
}

// allViews lists every view in tab order.
func allViews() []view {
	out := make([]view, 0, viewCount)
	for v := view(0); v < viewCount; v++ {
		out = append(out, v)
	}
	return out
}

// clampTrayScroll keeps the tray cursor and scroll offset within visible boundaries.
func (a *App) clampTrayScroll() {
	count := len(a.tray.items)
	if count == 0 {
		a.tray.cursor = 0
		a.tray.offset = 0
		return
	}
	a.tray.cursor = min(max(a.tray.cursor, 0), count-1)
	window := 10
	if a.tray.popupOpen {
		window = max((a.trayPopupLayout().body)/trayRowHeight, 1)
	} else if a.sidebarVisible() {
		window = a.traySidebarRows()
	}
	a.tray.offset = clampOffset(a.tray.offset, a.tray.cursor, window, count)
}

// updateTrayPopup handles keypresses, mouse scrolling, and clicks while the
// notification history popup is visible.
func (a *App) updateTrayPopup(msg tea.Msg) (tea.Cmd, bool) {
	keys := a.tray.keys
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, keys.Quit):
			return tea.Quit, true
		case key.Matches(msg, keys.Close):
			a.tray.popupOpen = false
			return nil, true
		case key.Matches(msg, keys.Open):
			return a.openNotificationPR(), true
		case key.Matches(msg, keys.Pin):
			return a.toggleSidebarMode(), true
		case key.Matches(msg, keys.Up):
			a.tray.cursor = max(a.tray.cursor-1, 0)
			a.clampTrayScroll()
			return nil, true
		case key.Matches(msg, keys.Down):
			a.tray.cursor = min(a.tray.cursor+1, max(len(a.tray.items)-1, 0))
			a.clampTrayScroll()
			return nil, true
		case key.Matches(msg, keys.Top):
			a.tray.cursor = 0
			a.clampTrayScroll()
			return nil, true
		case key.Matches(msg, keys.Bottom):
			a.tray.cursor = max(len(a.tray.items)-1, 0)
			a.clampTrayScroll()
			return nil, true
		case key.Matches(msg, keys.Copy):
			// Copied before closing: selectedTarget answers for the popup
			// only while it is open.
			cmd := a.copyURL()
			a.tray.popupOpen = false
			return cmd, true
		default:
			return nil, true
		}

	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			a.tray.cursor = max(a.tray.cursor-1, 0)
			a.clampTrayScroll()
			return nil, true
		case tea.MouseWheelDown:
			a.tray.cursor = min(a.tray.cursor+1, max(len(a.tray.items)-1, 0))
			a.clampTrayScroll()
			return nil, true
		}

	case tea.MouseClickMsg:
		if msg.Button != tea.MouseLeft {
			return nil, true
		}
		l := a.trayPopupLayout()
		if msg.X < l.x || msg.X >= l.x+l.width || msg.Y < l.y || msg.Y >= l.y+l.height {
			a.tray.popupOpen = false
			return nil, true
		}
		rowY := msg.Y - (l.y + 1) // top border = 1
		if rowY >= 0 && rowY < l.body {
			idx := a.tray.offset + rowY/trayRowHeight
			if idx >= 0 && idx < len(a.tray.items) {
				a.tray.cursor = idx
				return a.openNotificationPR(), true
			}
		}
		return nil, true
	}
	return nil, false
}

// trayPopupLayout holds the computed coordinates and dimensions of the centered modal.
type trayPopupLayout struct {
	width, height int
	x, y          int
	inner         int
	body          int
}

// trayPopupLayout calculates the dimensions, position, and scrollable body height
// of the notification history modal, ensuring the body fits a whole number of notification cards.
func (a *App) trayPopupLayout() trayPopupLayout {
	var l trayPopupLayout
	l.width = min(max(a.width-4, 1), 76)
	chrome := 4
	avail := max(a.height-chrome, 1)
	targetBody := min(avail, 18)
	if targetBody >= trayRowHeight {
		targetBody = (targetBody / trayRowHeight) * trayRowHeight
	}
	l.body = max(targetBody, 1)
	l.height = min(chrome+l.body, max(a.height, 1))
	l.inner = max(l.width-4, 1)
	l.x = max((a.width-l.width)/2, 0)
	l.y = max((a.height-l.height)/2, 0)
	return l
}

// renderTrayPopup floats the notification history modal over base.
func (a *App) renderTrayPopup(base []string) []string {
	l := a.trayPopupLayout()
	return a.floatOver(base, a.trayBox(l), l.x, l.y, l.width)
}

// trayBox builds the framed notification modal with title, rendered cards, and shortcut hints.
func (a *App) trayBox(l trayPopupLayout) []string {
	row := func(content string) string { return a.frameRow(content, l.inner) }

	box := make([]string, 0, l.height)
	box = append(box,
		a.edge(l.width, "╭", "╮", a.styles.OverlayTitle.Render("Notification History"), ""),
	)

	body := a.renderTrayItems(l.inner, l.body, true)
	for i := 0; i < l.body; i++ {
		line := ""
		if i < len(body) {
			line = body[i]
		}
		box = append(box, row(line))
	}
	keys := a.tray.keys
	hints := a.styles.Muted.Render(strings.Join([]string{
		hintFor(keys.Open), hintFor(keys.Pin), hintFor(keys.Close),
	}, " · "))
	box = append(box, a.edge(l.width, "╰", "╯", hints, ""))
	if len(box) > l.height {
		box = append(box[:max(l.height-1, 0)], box[len(box)-1])
	}
	return box
}

// renderTrayPane renders the persistent sidebar pane displaying notification history
// with an unread badge counter in the header.
func (a *App) renderTrayPane(width, height int) []string {
	inner := max(width, 1)
	lines := make([]string, 0, height)
	hdr := " " + a.styles.SectionHdr.Render("NOTIFICATIONS")
	if unread := a.tray.unreadCount(); unread > 0 {
		hdr += a.styles.Accent.Render(fmt.Sprintf(" (%d)", unread))
	}
	lines = append(lines,
		padTo(hdr, inner),
		padTo(" "+a.styles.PaneBorder.Render(strings.Repeat("─", max(inner-2, 1))), inner),
	)

	bodyHeight := max(height-len(lines), 0)
	body := a.renderTrayItems(inner, bodyHeight, a.focus == paneNotifications)
	for i := 0; i < bodyHeight; i++ {
		line := ""
		if i < len(body) {
			line = body[i]
		}
		lines = append(lines, padTo(line, inner))
	}
	return lines
}

// renderTrayItems renders the list of notification cards visible within the available height budget.
func (a *App) renderTrayItems(width, height int, active bool) []string {
	if len(a.tray.items) == 0 {
		return a.centeredNotice("no notifications yet", width, a.styles.Meta)
	}

	maxRows := max(height/trayRowHeight, 1)
	start := min(a.tray.offset, max(len(a.tray.items)-1, 0))
	end := min(start+maxRows, len(a.tray.items))

	lines := make([]string, 0, height)
	for i := start; i < end; i++ {
		item := a.tray.items[i]
		selected := active && i == a.tray.cursor
		lines = append(lines, a.renderTrayRow(item, width, selected)...)
	}
	return lines
}

// trayItemGlyph returns the semantic symbol and color style for a notification category.
func (a *App) trayItemGlyph(kind trayKind) seg {
	switch kind {
	case trayKindApproved:
		return seg{text: "✓", style: a.styles.Success}
	case trayKindChecks:
		return seg{text: "●", style: a.styles.Success}
	case trayKindHandoff:
		return seg{text: "»", style: a.styles.Accent}
	case trayKindHold:
		return seg{text: "⚑", style: a.styles.Error}
	case trayKindWatch:
		return seg{text: "◉", style: a.styles.Watch}
	default:
		return seg{text: "●", style: a.styles.Meta}
	}
}

// hintFor is how the popup's bottom edge names one of its keys.
func hintFor(b key.Binding) string {
	return b.Help().Key + ": " + b.Help().Desc
}

// trayItemAge formats the elapsed duration since the notification event
// occurred. It counts in minutes, which is all minutesShown wakes for.
func (a *App) trayItemAge(item trayItem) string {
	return model.HumanAge(a.now().Sub(item.at))
}

// trayKindStyle returns the accent color style for a notification category.
func (a *App) trayKindStyle(kind trayKind) lipgloss.Style {
	switch kind {
	case trayKindApproved, trayKindChecks:
		return a.styles.Success
	case trayKindHandoff:
		return a.styles.Accent
	case trayKindHold:
		return a.styles.Error
	case trayKindWatch:
		return a.styles.Watch
	default:
		return a.styles.Text
	}
}

// renderTrayRow formats a single notification entry as a 3-line card:
// Line 1: PR number, repository name, and relative age.
// Line 2: Category glyph and event headline (e.g. "✓ approved", "» handoff sent").
// Line 3: PR title and additional detail.
func (a *App) renderTrayRow(item trayItem, width int, selected bool) []string {
	inner := max(width-2, 1)
	prefix := "  "
	titleStyle := a.styles.Repo
	if selected {
		prefix = a.styles.SelectBar.Render("▌") + " "
		titleStyle = a.styles.Title
	}

	age := a.styles.Meta.Render(a.trayItemAge(item))

	// Line 1: Identity & age (#12 relloyd/prutil  now)
	var idSegs []seg
	if item.key.Number > 0 {
		idSegs = append(idSegs, seg{text: "#" + fmt.Sprint(item.key.Number), style: a.styles.Number})
	}
	if item.key.Repo != "" {
		idSegs = append(idSegs, seg{text: item.key.Repo, style: titleStyle})
	} else if item.key.Number == 0 {
		if item.title != "" {
			idSegs = append(idSegs, seg{text: item.title, style: titleStyle})
		} else {
			idSegs = append(idSegs, seg{text: "notification", style: titleStyle})
		}
	}
	availID := max(inner-lenOf(age)-1, 1)
	identity := fitSegs(availID, " ", idSegs...)
	line1 := prefix + justify(inner, identity, age)

	// Line 2: Notification type (e.g. ✓ approved, » handoff sent)
	glyph := a.trayItemGlyph(item.kind)
	kindStyle := a.trayKindStyle(item.kind)
	typeText := item.headline
	if typeText == "" {
		switch item.kind {
		case trayKindApproved:
			typeText = "approved"
		case trayKindChecks:
			typeText = "checks passed"
		case trayKindHandoff:
			typeText = "handoff"
		case trayKindHold:
			typeText = "held"
		case trayKindWatch:
			typeText = "watch"
		default:
			typeText = "notification"
		}
	}
	typeContent := fitSegs(inner, " ", glyph, seg{text: typeText, style: kindStyle})
	line2 := prefix + padTo(typeContent, inner)

	// Line 3: PR Title and detail (indented 2 spaces under the type)
	summary := item.title
	if item.detail != "" && item.detail != item.title {
		if summary != "" {
			summary += " · " + item.detail
		} else {
			summary = item.detail
		}
	}
	availSummary := max(inner-2, 1)
	line3 := prefix + padTo("  "+a.styles.Muted.Render(ansi.Truncate(summary, availSummary, ellipsis)), inner)

	return []string{
		padTo(line1, width),
		padTo(line2, width),
		padTo(line3, width),
	}
}

// traySidebarRows is how many notification cards the sidebar has room for.
// The click mapping and the scrolling both ask it, so a click can only land
// on a card that is drawn.
func (a *App) traySidebarRows() int {
	return max((a.bodyHeight()-trayPaneHeader)/trayRowHeight, 1)
}
