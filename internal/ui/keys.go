package ui

import "charm.land/bubbles/v2/key"

// keyMap collects every binding in the app so that the help view and the
// update loop cannot drift apart.
type keyMap struct {
	Up      key.Binding
	Down    key.Binding
	Top     key.Binding
	Bottom  key.Binding
	Into    key.Binding
	Back    key.Binding
	Open    key.Binding
	Copy    key.Binding
	Refresh key.Binding
	Auto    key.Binding
	Watch   key.Binding
	Handoff key.Binding
	Notify  key.Binding
	NextTab key.Binding
	Help    key.Binding
	Quit    key.Binding
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
		Notify: key.NewBinding(
			key.WithKeys("N"),
			key.WithHelp("N", "notify new feedback"),
		),
		NextTab: key.NewBinding(
			key.WithKeys("tab"),
			key.WithHelp("tab", "open/closed"),
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

// FullHelp implements help.KeyMap.
func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.Top, k.Bottom},
		{k.Into, k.Back, k.Open, k.Copy},
		{k.Refresh, k.Auto, k.NextTab},
		{k.Watch, k.Handoff, k.Notify},
		{k.Help, k.Quit},
	}
}
