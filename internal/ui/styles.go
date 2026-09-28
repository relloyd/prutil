package ui

import (
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// Styles holds every colour and text style the TUI uses. Keeping them in one
// place means the palette can be retuned without touching layout code.
type Styles struct {
	Title      lipgloss.Style
	Meta       lipgloss.Style
	Muted      lipgloss.Style
	Text       lipgloss.Style
	Accent     lipgloss.Style
	SelectBar  lipgloss.Style
	Number     lipgloss.Style
	Repo       lipgloss.Style
	Branch     lipgloss.Style
	Arrow      lipgloss.Style
	PaneBorder lipgloss.Style
	Header     lipgloss.Style
	Mode       lipgloss.Style
	Auto       lipgloss.Style
	Watch      lipgloss.Style
	// Adopted marks a pull request somebody else opened that the reader has
	// taken on, in a colour nothing else uses, so that it is never mistaken
	// for one of their own.
	Adopted    lipgloss.Style
	Help       lipgloss.Style
	Error      lipgloss.Style
	Status     lipgloss.Style
	SectionHdr lipgloss.Style

	// The ? overlay: its frame, the title set into it, the key column, the
	// characters a filter matched, and the dimmed screen behind it.
	OverlayBorder lipgloss.Style
	OverlayTitle  lipgloss.Style
	OverlayKey    lipgloss.Style
	MatchHit      lipgloss.Style
	Backdrop      lipgloss.Style

	BadgeDraft    lipgloss.Style
	BadgeMerged   lipgloss.Style
	BadgeClosed   lipgloss.Style
	BadgeConflict lipgloss.Style
	BadgeApproved lipgloss.Style
	BadgeChanges  lipgloss.Style
	BadgeReview   lipgloss.Style

	Success  lipgloss.Style
	Failure  lipgloss.Style
	Pending  lipgloss.Style
	Neutral  lipgloss.Style
	Selected lipgloss.Style
}

// newStyles builds the palette for a light or dark terminal. The colours are a
// modern, high-contrast set: indigo for structure, teal for identifiers, and
// the conventional green / red / amber for check state. Mode is deliberately
// the brightest thing in the header: it says which view is on screen, and the
// muted grey the rest of the meta line uses was too easy to overlook.
func newStyles(isDark bool) Styles {
	c := lipgloss.LightDark(isDark)

	var (
		accent   = c(lipgloss.Color("#5B4BE8"), lipgloss.Color("#A78BFA"))
		teal     = c(lipgloss.Color("#0F766E"), lipgloss.Color("#2DD4BF"))
		text     = c(lipgloss.Color("#1F2328"), lipgloss.Color("#E7E7EA"))
		muted    = c(lipgloss.Color("#6B7280"), lipgloss.Color("#8B93A7"))
		faint    = c(lipgloss.Color("#8C93A0"), lipgloss.Color("#6B7280"))
		border   = c(lipgloss.Color("#D0D7DE"), lipgloss.Color("#3A3F4B"))
		green    = c(lipgloss.Color("#15803D"), lipgloss.Color("#4ADE80"))
		red      = c(lipgloss.Color("#DC2626"), lipgloss.Color("#F87171"))
		amber    = c(lipgloss.Color("#B45309"), lipgloss.Color("#FBBF24"))
		grey     = c(lipgloss.Color("#7A8290"), lipgloss.Color("#9CA3AF"))
		selected = c(lipgloss.Color("#EEF0FF"), lipgloss.Color("#262338"))
		pink     = c(lipgloss.Color("#BE185D"), lipgloss.Color("#F472B6"))
	)

	base := lipgloss.NewStyle()
	badge := base.Bold(true)

	return Styles{
		Title:      base.Foreground(text).Bold(true),
		Meta:       base.Foreground(muted),
		Muted:      base.Foreground(muted),
		Text:       base.Foreground(text),
		Accent:     base.Foreground(accent).Bold(true),
		SelectBar:  base.Foreground(accent),
		Number:     base.Foreground(accent),
		Repo:       base.Foreground(teal),
		Branch:     base.Foreground(muted),
		Arrow:      base.Foreground(faint),
		PaneBorder: base.Foreground(border),
		Header:     base.Foreground(accent).Bold(true),
		Mode:       base.Foreground(text).Bold(true),
		Auto:       base.Foreground(amber).Bold(true),
		Watch:      base.Foreground(amber).Bold(true),
		Adopted:    base.Foreground(pink).Bold(true),
		Help:       base.Foreground(faint),
		Error:      base.Foreground(red).Bold(true),
		Status:     base.Foreground(amber),
		SectionHdr: base.Foreground(muted).Bold(true),

		OverlayBorder: base.Foreground(accent),
		OverlayTitle:  base.Foreground(text).Bold(true),
		OverlayKey:    base.Foreground(teal).Bold(true),
		MatchHit:      base.Foreground(amber).Bold(true).Underline(true),
		Backdrop:      base.Foreground(border),

		BadgeDraft:    badge.Foreground(grey),
		BadgeMerged:   badge.Foreground(accent),
		BadgeClosed:   badge.Foreground(red),
		BadgeConflict: badge.Foreground(red),
		BadgeApproved: badge.Foreground(green),
		BadgeChanges:  badge.Foreground(amber),
		BadgeReview:   badge.Foreground(muted),

		Success:  base.Foreground(green),
		Failure:  base.Foreground(red),
		Pending:  base.Foreground(amber),
		Neutral:  base.Foreground(grey),
		Selected: base.Background(selected),
	}
}

// helpInput styles the overlay's filter from the palette. The cursor does not
// blink: a blinking cursor is a timer that runs for as long as the overlay is
// open, and a redraw loop is not worth a flashing block.
func (s Styles) helpInput() textinput.Styles {
	state := textinput.StyleState{Text: s.Title, Placeholder: s.Muted, Prompt: s.Accent}
	return textinput.Styles{
		Focused: state,
		Blurred: state,
		Cursor:  textinput.CursorStyle{Color: s.Accent.GetForeground(), Shape: tea.CursorBlock},
	}
}
