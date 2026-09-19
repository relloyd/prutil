package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/relloyd/prutil/internal/model"
)

// keyMap collects every binding in the app so that the help view and the
// update loop cannot drift apart.
type keyMap struct {
	Up            key.Binding
	Down          key.Binding
	Top           key.Binding
	Bottom        key.Binding
	Into          key.Binding
	Back          key.Binding
	Open          key.Binding
	Copy          key.Binding
	Refresh       key.Binding
	Auto          key.Binding
	Watch         key.Binding
	Handoff       key.Binding
	CheckHandoff  key.Binding
	TriggerReview key.Binding
	Notify        key.Binding
	NextTab       key.Binding
	Settings      key.Binding
	Help          key.Binding
	Quit          key.Binding
}

// defaultKeys returns the bindings described in the README.
func defaultKeys() keyMap {
	return keyMap{
		Up: key.NewBinding(
			key.WithKeys("up", "k"),
			key.WithHelp("↑/k", "up"),
		),
		Down: key.NewBinding(
			key.WithKeys("down", "j"),
			key.WithHelp("↓/j", "down"),
		),
		Top: key.NewBinding(
			key.WithKeys("g", "home"),
			key.WithHelp("g", "top"),
		),
		Bottom: key.NewBinding(
			key.WithKeys("G", "end"),
			key.WithHelp("G", "bottom"),
		),
		Into: key.NewBinding(
			key.WithKeys("right", "l"),
			key.WithHelp("→/l", "detail"),
		),
		Back: key.NewBinding(
			key.WithKeys("left", "h", "esc"),
			key.WithHelp("←/h", "back"),
		),
		Open: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "browser"),
		),
		Copy: key.NewBinding(
			key.WithKeys("y", "c"),
			key.WithHelp("y", "copy URL"),
		),
		Refresh: key.NewBinding(
			key.WithKeys("r"),
			key.WithHelp("r", "refresh"),
		),
		Auto: key.NewBinding(
			key.WithKeys("a"),
			key.WithHelp("a", "auto-refresh"),
		),
		Watch: key.NewBinding(
			key.WithKeys("w"),
			key.WithHelp("w", "watch"),
		),
		Handoff: key.NewBinding(
			key.WithKeys("W"),
			key.WithHelp("W", "hand to agent"),
		),
		CheckHandoff: key.NewBinding(
			key.WithKeys("F"),
			key.WithHelp("F", "investigate checks"),
		),
		TriggerReview: key.NewBinding(
			key.WithKeys("R"),
			key.WithHelp("R", "trigger AI review"),
		),
		Notify: key.NewBinding(
			key.WithKeys("N"),
			key.WithHelp("N", "notify new feedback"),
		),
		NextTab: key.NewBinding(
			key.WithKeys("tab"),
			key.WithHelp("tab", "open/closed"),
		),
		// s for settings, and , because that is where the settings are in
		// every macOS application.
		Settings: key.NewBinding(
			key.WithKeys("s", ","),
			key.WithHelp("s", "settings"),
		),
		Help: key.NewBinding(
			key.WithKeys("?"),
			key.WithHelp("?", "help"),
		),
		Quit: key.NewBinding(
			key.WithKeys("q", "ctrl+c"),
			key.WithHelp("q", "quit"),
		),
	}
}

// ShortHelp implements help.KeyMap. It is the footer, which has one line to
// work with, so it names the actions and leaves moving about to the arrow keys
// and to the full list under ?. Whatever is added here, keep q quit inside 120
// columns: the help component drops the tail that does not fit, and quit is
// the one binding a reader must never have to hunt for.
//
// Back is not in it. It is the obvious mirror of the binding that goes into
// the detail pane, esc does the same thing, and the room it frees is what lets
// the watch key be named here instead of only under ?.
func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Into, k.Open, k.Copy, k.NextTab, k.Refresh, k.Auto, k.Watch, k.Help, k.Quit}
}

// FullHelp implements help.KeyMap. Its groups are the overlay's sections, so
// the two can never disagree about what belongs together.
func (k keyMap) FullHelp() [][]key.Binding {
	sections := k.helpSections(false)
	groups := make([][]key.Binding, 0, len(sections))
	for _, section := range sections {
		group := make([]key.Binding, 0, len(section.entries))
		for _, entry := range section.entries {
			group = append(group, entry.binding)
		}
		groups = append(groups, group)
	}
	return groups
}

// helpEntry is one line of the ? overlay: something the reader can press, a
// few words naming it, and a sentence explaining it.
type helpEntry struct {
	binding key.Binding
	// keys names what is pressed when it is not a key binding at all, such as
	// a mouse click. An entry without a binding cannot be run from the overlay.
	keys   string
	title  string
	detail string
}

// label is the key column of the overlay: every key the binding answers to,
// so an alias can never go unmentioned the way it could in a hand-written
// help string.
func (e helpEntry) label() string {
	if e.keys != "" {
		return e.keys
	}
	return keyLabel(e.binding)
}

// runnable reports whether the overlay can press this entry's key on the
// reader's behalf.
func (e helpEntry) runnable() bool {
	return len(e.binding.Keys()) > 0
}

// searchText is everything the overlay says about an entry, which a query is
// looked for in once titles and keys have been fuzzy-matched.
func (e helpEntry) searchText(section string) string {
	return strings.Join([]string{e.title, e.label(), e.detail, section}, " ")
}

// helpSection is a titled group of entries in the overlay.
type helpSection struct {
	title   string
	entries []helpEntry
}

// helpSections is everything the ? overlay lists, in the order it lists it.
// Every binding in keyMap belongs in exactly one section; a test fails when
// one is missing. The mouse section only appears when prutil asked the
// terminal for the mouse.
func (k keyMap) helpSections(mouse bool) []helpSection {
	sections := []helpSection{
		{title: "Navigation", entries: []helpEntry{
			{binding: k.Up, title: "move up",
				detail: "Move up within the focused pane. In the detail pane this selects WATCH or CHECKS."},
			{binding: k.Down, title: "move down",
				detail: "Move down within the focused pane. In the detail pane this selects WATCH or CHECKS."},
			{binding: k.Top, title: "jump to the top",
				detail: "Jump to the first item in the focused pane."},
			{binding: k.Bottom, title: "jump to the bottom",
				detail: "Jump to the last item in the focused pane."},
			{binding: k.Into, title: "focus detail / drill in",
				detail: "Focus the detail pane from the list, or drill into the selected detail section."},
			{binding: k.Back, title: "go back",
				detail: "Go back one level: out of an expanded section, or from the detail pane to the list."},
		}},
		{title: "Pull requests", entries: []helpEntry{
			{binding: k.Open, title: "open in browser",
				detail: "Open the selected pull request, or the selected check, in your browser."},
			{binding: k.Copy, title: "copy URL",
				detail: "Copy the selected pull request's URL, or the selected check's, to the clipboard."},
			{binding: k.NextTab, title: "switch open / closed",
				detail: "Switch between your open and your recently closed pull requests."},
		}},
		{title: "Refreshing", entries: []helpEntry{
			{binding: k.Refresh, title: "refresh",
				detail: "Reload the list and its checks from GitHub, and wake anything the watcher has backed off."},
			{binding: k.Auto, title: "auto-refresh",
				detail: fmt.Sprintf("Reload every %s, %d times over. Press again to add %d more.",
					model.HumanDuration(autoRefreshInterval), autoRefreshBurst, autoRefreshBurst)},
		}},
		{title: "Watching and agents", entries: []helpEntry{
			{binding: k.Watch, title: "watch / unwatch",
				detail: "Watch the selected pull request for review feedback and failing checks, or stop watching it."},
			{binding: k.Handoff, title: "hand to agent",
				detail: "Hand the selected pull request's open review feedback to a coding agent now, creating one when needed."},
			{binding: k.CheckHandoff, title: "investigate failed checks",
				detail: "Investigate the selected pull request's failed checks now. This never creates an agent."},
			{binding: k.TriggerReview, title: "trigger AI review",
				detail: "Trigger an AI review on the selected open pull request by posting the configured comment. Press twice to confirm."},
			{binding: k.Notify, title: "notify new feedback",
				detail: "Check the selected open pull request for new review feedback and notify an existing agent."},
		}},
		{title: "General", entries: []helpEntry{
			{binding: k.Settings, title: "settings",
				detail: "View and edit prutil configuration settings, including notifications, watch polling intervals, AI review prompts, and herdr integration. Each change is saved to the configuration file as it is made."},
			{binding: k.Help, title: "keyboard shortcuts",
				detail: "Open this list. Type to filter, enter to run the highlighted shortcut, esc to close."},
			{binding: k.Quit, title: "quit",
				detail: "Quit prutil. Watched pull requests stay watched for next time."},
		}},
	}
	if mouse {
		sections = append(sections, helpSection{title: "Mouse", entries: []helpEntry{
			{keys: "click", title: "select a pull request",
				detail: "Left click a row in the list to select it."},
			{keys: "wheel", title: "scroll",
				detail: "Scroll whichever pane the pointer is over."},
		}})
	}
	return sections
}

// keyGlyphs are the keys the overlay draws as arrows rather than by name.
var keyGlyphs = map[string]string{"up": "↑", "down": "↓", "left": "←", "right": "→"}

// keyGlyph is how the overlay and the README write one key.
func keyGlyph(k string) string {
	if glyph, ok := keyGlyphs[k]; ok {
		return glyph
	}
	return k
}

// keyLabel names every key a binding answers to, in the order it lists them.
func keyLabel(b key.Binding) string {
	keys := b.Keys()
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, keyGlyph(k))
	}
	return strings.Join(out, "/")
}

// namedKeys are the key names a binding can use that are not a character.
var namedKeys = map[string]rune{
	"enter": tea.KeyEnter,
	"space": tea.KeySpace,
	"tab":   tea.KeyTab,
	"esc":   tea.KeyEscape,
	"up":    tea.KeyUp,
	"down":  tea.KeyDown,
	"left":  tea.KeyLeft,
	"right": tea.KeyRight,
	"home":  tea.KeyHome,
	"end":   tea.KeyEnd,
}

// keyPress builds the key message a binding's key string stands for. It is
// how the overlay runs a shortcut: it presses the key on the reader's behalf.
func keyPress(s string) tea.KeyPressMsg {
	if code, ok := namedKeys[s]; ok {
		return tea.KeyPressMsg{Code: code}
	}
	if rest, ok := strings.CutPrefix(s, "ctrl+"); ok && rest != "" {
		return tea.KeyPressMsg{Code: []rune(rest)[0], Mod: tea.ModCtrl}
	}
	return tea.KeyPressMsg{Code: []rune(s)[0], Text: s}
}

// overlayKeyMap holds the keys the ? overlay reads for itself. Everything else
// typed while it is open goes to its filter, which is why these are kept apart
// from keyMap: q has to reach the filter, so the overlay cannot share the
// app's quit binding.
type overlayKeyMap struct {
	Close    key.Binding
	Run      key.Binding
	Up       key.Binding
	Down     key.Binding
	PageUp   key.Binding
	PageDown key.Binding
	// HalfPageUp and HalfPageDown are the vim and less chords. They take
	// ctrl+u and ctrl+d from the filter, which would otherwise delete to the
	// start of the query and delete forward; backspace and ctrl+w still edit.
	HalfPageUp   key.Binding
	HalfPageDown key.Binding
	Quit         key.Binding
}

// defaultOverlayKeys returns the bindings the overlay reads before its filter.
func defaultOverlayKeys() overlayKeyMap {
	return overlayKeyMap{
		Close: key.NewBinding(
			key.WithKeys("esc", "?"),
			key.WithHelp("esc", "close"),
		),
		Run: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "run"),
		),
		Up: key.NewBinding(
			key.WithKeys("up", "ctrl+p"),
			key.WithHelp("↑", "up"),
		),
		Down: key.NewBinding(
			key.WithKeys("down", "ctrl+n"),
			key.WithHelp("↓", "down"),
		),
		PageUp: key.NewBinding(
			key.WithKeys("pgup"),
			key.WithHelp("pgup", "page up"),
		),
		PageDown: key.NewBinding(
			key.WithKeys("pgdown"),
			key.WithHelp("pgdown", "page down"),
		),
		HalfPageUp: key.NewBinding(
			key.WithKeys("ctrl+u"),
			key.WithHelp("ctrl+u", "half page up"),
		),
		HalfPageDown: key.NewBinding(
			key.WithKeys("ctrl+d"),
			key.WithHelp("ctrl+d", "half page down"),
		),
		Quit: key.NewBinding(
			key.WithKeys("ctrl+c"),
			key.WithHelp("ctrl+c", "quit"),
		),
	}
}

// settingsKeyMap holds the keys the settings pane reads while it is open. They
// are apart from keyMap for the same reason as the overlay's: nothing pressed
// in the pane should reach the list behind it.
type settingsKeyMap struct {
	Close     key.Binding
	Toggle    key.Binding
	Edit      key.Binding
	CycleNext key.Binding
	CyclePrev key.Binding
	StepUp    key.Binding
	StepDown  key.Binding
	Default   key.Binding
	NextSec   key.Binding
	PrevSec   key.Binding
	Test      key.Binding
	Up        key.Binding
	Down      key.Binding
	Top       key.Binding
	Bottom    key.Binding
	Quit      key.Binding
}

// defaultSettingsKeys returns the bindings the settings pane reads. The keys
// that open it also close it, the way ? does for the overlay, and so does q:
// a reader pressing it in a pane of settings wants the pane gone, and what
// they changed is already saved either way.
func defaultSettingsKeys() settingsKeyMap {
	return settingsKeyMap{
		Close: key.NewBinding(
			key.WithKeys("esc", "s", ",", "q"),
			key.WithHelp("esc", "close"),
		),
		Toggle: key.NewBinding(
			key.WithKeys("space", "enter", "x"),
			key.WithHelp("space", "toggle"),
		),
		Edit: key.NewBinding(
			key.WithKeys("enter", "e"),
			key.WithHelp("enter", "edit"),
		),
		CycleNext: key.NewBinding(
			key.WithKeys("right", "l"),
			key.WithHelp("→", "next"),
		),
		CyclePrev: key.NewBinding(
			key.WithKeys("left", "h"),
			key.WithHelp("←", "prev"),
		),
		StepUp: key.NewBinding(
			key.WithKeys("+", "]"),
			key.WithHelp("+", "increase"),
		),
		StepDown: key.NewBinding(
			key.WithKeys("-", "["),
			key.WithHelp("-", "decrease"),
		),
		Default: key.NewBinding(
			key.WithKeys("d"),
			key.WithHelp("d", "default"),
		),
		NextSec: key.NewBinding(
			key.WithKeys("tab"),
			key.WithHelp("tab", "next section"),
		),
		PrevSec: key.NewBinding(
			key.WithKeys("shift+tab"),
			key.WithHelp("shift+tab", "prev section"),
		),
		Test: key.NewBinding(
			key.WithKeys("t"),
			key.WithHelp("t", "test notification"),
		),
		Up: key.NewBinding(
			key.WithKeys("up", "k", "ctrl+p"),
			key.WithHelp("↑", "up"),
		),
		Down: key.NewBinding(
			key.WithKeys("down", "j", "ctrl+n"),
			key.WithHelp("↓", "down"),
		),
		Top: key.NewBinding(
			key.WithKeys("g", "home"),
			key.WithHelp("g", "top"),
		),
		Bottom: key.NewBinding(
			key.WithKeys("G", "end"),
			key.WithHelp("G", "bottom"),
		),
		Quit: key.NewBinding(
			key.WithKeys("ctrl+c"),
			key.WithHelp("ctrl+c", "quit"),
		),
	}
}
