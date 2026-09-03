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
	Refresh key.Binding
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
			key.WithHelp("→/l", "checks"),
		),
		Back: key.NewBinding(
			key.WithKeys("left", "h", "esc"),
			key.WithHelp("←/h", "back"),
		),
		Open: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "open in browser"),
		),
		Refresh: key.NewBinding(
			key.WithKeys("r"),
			key.WithHelp("r", "refresh"),
		),
		NextTab: key.NewBinding(
			key.WithKeys("tab"),
			key.WithHelp("tab", "next view"),
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

// ShortHelp implements help.KeyMap.
func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Into, k.Back, k.Open, k.Refresh, k.Help, k.Quit}
}

// FullHelp implements help.KeyMap.
func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.Top, k.Bottom},
		{k.Into, k.Back, k.Open},
		{k.Refresh, k.NextTab, k.Help, k.Quit},
	}
}
