package ui

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// typeInto presses each character of text in turn, the way the reader types
// into the overlay's filter.
func typeInto(t *testing.T, app *App, text string) {
	t.Helper()
	for _, r := range text {
		send(t, app, press(string(r)))
	}
}

// openOverlay opens the shortcut overlay and types a query into it.
func openOverlay(t *testing.T, app *App, query string) {
	t.Helper()
	send(t, app, press("?"))
	require.True(t, app.overlay.open, "? opens the overlay")
	typeInto(t, app, query)
}

// firstTitle is the title of the entry the overlay ranked first.
func firstTitle(t *testing.T, app *App) string {
	t.Helper()
	require.NotEmpty(t, app.overlay.matches, "the query matched something")
	return app.overlay.matches[0].entry.title
}

func TestEveryBindingIsListedInTheShortcutOverlay(t *testing.T) {
	keys := defaultKeys()
	listed := map[string]bool{}
	for _, section := range keys.helpSections(false) {
		for _, entry := range section.entries {
			listed[strings.Join(entry.binding.Keys(), " ")] = true
		}
	}

	v := reflect.ValueOf(keys)
	for i := 0; i < v.NumField(); i++ {
		binding, ok := v.Field(i).Interface().(key.Binding)
		require.Truef(t, ok, "keyMap.%s is a key.Binding", v.Type().Field(i).Name)
		assert.Truef(t, listed[strings.Join(binding.Keys(), " ")],
			"keyMap.%s has no entry in helpSections, so ? would not list it", v.Type().Field(i).Name)
	}
}

func TestTheReadmeKeysTableNamesEveryBindingsKeys(t *testing.T) {
	readme, err := os.ReadFile("../../README.md")
	require.NoError(t, err)
	_, table, found := strings.Cut(string(readme), "## Keys\n")
	require.True(t, found, "README has a Keys section")
	table, _, _ = strings.Cut(table, "\n## ")

	for _, section := range defaultKeys().helpSections(false) {
		for _, entry := range section.entries {
			for _, k := range entry.binding.Keys() {
				assert.Containsf(t, table, "`"+keyGlyph(k)+"`",
					"README's Keys table does not mention %q, which %s answers to", k, entry.title)
			}
		}
	}
}

func TestEveryShortcutTheOverlayRunsPressesItsOwnBinding(t *testing.T) {
	for _, section := range defaultKeys().helpSections(true) {
		for _, entry := range section.entries {
			if !entry.runnable() {
				continue
			}
			first := entry.binding.Keys()[0]
			assert.Truef(t, key.Matches(keyPress(first), entry.binding),
				"pressing %q on the reader's behalf does not trigger %s", first, entry.title)
		}
	}
}

func TestQuestionMarkOpensTheOverlayWithTheFilterReadyAndEscClosesIt(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	send(t, app, press("j"))
	send(t, app, press("l"))
	focus, cursor := app.focus, app.cur().cursor

	cmd := send(t, app, press("?"))
	assert.Nil(t, cmd, "opening the overlay starts no timer, which a blinking cursor would")
	require.True(t, app.overlay.open)
	assert.True(t, app.overlay.input.Focused(), "the filter has the keyboard straight away")
	assert.Len(t, app.overlay.matches, app.overlay.total, "nothing is filtered before anything is typed")
	assert.Contains(t, plain(app.render()), "Keyboard shortcuts")

	send(t, app, press("esc"))
	assert.False(t, app.overlay.open)
	assert.Equal(t, focus, app.focus, "esc closes the overlay rather than going back a pane")
	assert.Equal(t, cursor, app.cur().cursor)
	assert.NotContains(t, plain(app.render()), "Keyboard shortcuts")
}

func TestQuestionMarkClosesTheOverlayEvenWithAQueryTyped(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	openOverlay(t, app, "wat")

	send(t, app, press("?"))
	assert.False(t, app.overlay.open)

	send(t, app, press("?"))
	assert.Empty(t, app.overlay.input.Value(), "the next open starts from an empty filter")
	assert.Len(t, app.overlay.matches, app.overlay.total)
}

func TestTypingFiltersTheOverlayImmediately(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	openOverlay(t, app, "cop")

	assert.Equal(t, "copy URL", firstTitle(t, app))
	assert.Less(t, len(app.overlay.matches), app.overlay.total)
	assert.Contains(t, plain(app.render()), "copy URL")
}

func TestAnExplanationOnlyMatchesWhenItContainsTheQuery(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	openOverlay(t, app, "agent")

	titles := make([]string, 0, len(app.overlay.matches))
	for _, m := range app.overlay.matches {
		titles = append(titles, m.entry.title)
	}
	assert.Equal(t, "hand to agent", firstTitle(t, app), "a title match ranks above an explanation that mentions it")
	assert.ElementsMatch(t, []string{
		"hand to agent", "watch / unwatch", "investigate failed checks", "trigger AI review", "notify new feedback",
	}, titles, "the agent shortcuts are found through their section and explanations")
	assert.NotContains(t, titles, "refresh", "letters scattered across a long sentence are not a match")
	assert.NotContains(t, titles, "go back")
}

func TestAKeyTypedExactlyRanksItsOwnShortcutFirst(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  string
	}{
		{name: "a capital W finds the handoff rather than watching", query: "W", want: "hand to agent"},
		{name: "a lower-case w finds watching rather than the handoff", query: "w", want: "watch / unwatch"},
		{name: "a capital G finds the bottom", query: "G", want: "jump to the bottom"},
		{name: "a named key such as tab finds its shortcut", query: "tab", want: "switch open / closed"},
		{name: "a chord typed out finds the binding that answers to it", query: "ctrl+c", want: "quit"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			app, _, _ := newTestApp(t, 120, 40)
			openOverlay(t, app, tc.query)
			assert.Equal(t, tc.want, firstTitle(t, app))
		})
	}
}

func TestQTypesIntoTheFilterAndCtrlCStillQuits(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	send(t, app, press("?"))

	msgs := drain(send(t, app, press("q")))
	assert.NotContains(t, msgs, tea.QuitMsg{}, "q is a letter while the overlay is open")
	assert.True(t, app.overlay.open)
	assert.Equal(t, "q", app.overlay.input.Value())

	assert.Contains(t, drain(send(t, app, press("ctrl+c"))), tea.QuitMsg{})
}

func TestKeysDoNotReachTheAppWhileTheOverlayIsOpen(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	gen, active, cursor, focus := app.gen, app.active, app.cur().cursor, app.focus

	openOverlay(t, app, "rjGal")
	send(t, app, press("tab"))
	send(t, app, press("right"))

	assert.Equal(t, gen, app.gen, "r did not refresh")
	assert.Equal(t, active, app.active, "tab did not switch the view")
	assert.Equal(t, cursor, app.cur().cursor, "j and G did not move the list")
	assert.Equal(t, focus, app.focus, "l and → did not focus the detail pane")
	assert.Zero(t, app.autoLeft, "a did not start auto-refresh")
	assert.Equal(t, "rjGal", app.overlay.input.Value())
}

func TestEnterRunsTheHighlightedShortcut(t *testing.T) {
	tests := []struct {
		name  string
		query string
		check func(t *testing.T, app *App, cmd tea.Cmd, gen int)
	}{
		{
			name:  "copy URL copies the selected pull request's link",
			query: "copy",
			check: func(t *testing.T, app *App, cmd tea.Cmd, _ int) {
				drain(cmd)
				pr, ok := app.selectedPR()
				require.True(t, ok)
				assert.Equal(t, []string{pr.URL}, clipboardOf(t, app).copied())
			},
		},
		{
			name:  "refresh starts a refresh",
			query: "r",
			check: func(t *testing.T, app *App, _ tea.Cmd, gen int) {
				assert.Equal(t, gen+1, app.gen)
			},
		},
		{
			name:  "switching open and closed switches the view",
			query: "tab",
			check: func(t *testing.T, app *App, _ tea.Cmd, _ int) {
				assert.Equal(t, viewClosed, app.active)
			},
		},
		{
			name:  "the shortcut list itself only closes the overlay",
			query: "keyboard",
			check: func(t *testing.T, app *App, cmd tea.Cmd, gen int) {
				assert.Nil(t, cmd)
				assert.Equal(t, gen, app.gen)
			},
		},
		{
			name:  "a mouse entry has no key to press and only closes the overlay",
			query: "wheel",
			check: func(t *testing.T, _ *App, cmd tea.Cmd, _ int) {
				assert.Nil(t, cmd)
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			app, _, _ := newTestApp(t, 120, 40)
			openOverlay(t, app, tc.query)
			gen := app.gen

			cmd := send(t, app, press("enter"))
			assert.False(t, app.overlay.open, "running a shortcut closes the overlay")
			tc.check(t, app, cmd, gen)
		})
	}
}

func TestEnterWithNothingMatchedLeavesTheOverlayOpen(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	openOverlay(t, app, "zzzz")

	assert.Empty(t, app.overlay.matches)
	assert.Contains(t, plain(app.render()), "no shortcut matches “zzzz”")

	assert.Nil(t, send(t, app, press("enter")))
	assert.True(t, app.overlay.open)
}

func TestTheOverlaySelectionStaysInsideTheListAndOnScreen(t *testing.T) {
	app, _, _ := newTestApp(t, 100, 20)
	openOverlay(t, app, "")
	o := &app.overlay

	send(t, app, press("up"))
	assert.Zero(t, o.cursor, "up at the top stays put")

	window := app.helpLayout().window
	require.Less(t, window, len(o.rows), "the list must overflow the window for this test to mean anything")
	for i := 0; i < o.total+5; i++ {
		send(t, app, press("down"))
		row := o.rowOf[o.cursor]
		require.GreaterOrEqual(t, row, o.offset, "the selection scrolled above the window")
		require.Less(t, row, o.offset+window, "the selection scrolled below the window")
	}
	assert.Equal(t, o.total-1, o.cursor, "down at the bottom stays put")
	assert.Contains(t, plain(app.render()), o.matches[o.cursor].entry.title)

	for i := 0; i < o.total; i++ {
		send(t, app, press("up"))
	}
	assert.Zero(t, o.offset, "back at the top, the first heading is on screen again")
	assert.Contains(t, plain(app.render()), "NAVIGATION")
}

func TestCtrlDAndCtrlUMoveTheOverlayByHalfAPageWithoutEditingTheFilter(t *testing.T) {
	app, _, _ := newTestApp(t, 100, 20)
	openOverlay(t, app, "")
	o := &app.overlay
	half := app.helpLayout().window / 2
	require.Greater(t, half, 1, "half a page must be more than one row for this test to mean anything")

	send(t, app, keyPress("ctrl+u"))
	assert.Zero(t, o.cursor, "ctrl+u at the top stays put")

	for _, step := range []struct {
		chord string
		want  int
	}{{"ctrl+d", half}, {"ctrl+d", 2 * half}, {"ctrl+u", half}} {
		send(t, app, keyPress(step.chord))
		assert.Equalf(t, step.want, o.cursor, "after %s", step.chord)
		row := o.rowOf[o.cursor]
		assert.GreaterOrEqual(t, row, o.offset, "the selection stays on screen")
		assert.Less(t, row, o.offset+app.helpLayout().window, "the selection stays on screen")
	}

	for i := 0; i < o.total; i++ {
		send(t, app, keyPress("ctrl+d"))
	}
	assert.Equal(t, o.total-1, o.cursor, "ctrl+d at the bottom stays put")

	app, _, _ = newTestApp(t, 100, 20)
	openOverlay(t, app, "e")
	send(t, app, keyPress("ctrl+u"))
	send(t, app, keyPress("ctrl+d"))
	assert.Equal(t, "e", app.overlay.input.Value(), "the chords scroll rather than deleting from the filter")
}

func TestTheMouseScrollsTheOverlayAndCannotClickThroughIt(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	openOverlay(t, app, "")
	cursor := app.cur().cursor

	send(t, app, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	assert.Equal(t, 1, app.overlay.cursor, "the wheel moves the overlay's selection")

	send(t, app, click(2, headerHeight+rowHeight))
	assert.True(t, app.overlay.open)
	assert.Equal(t, cursor, app.cur().cursor, "a click lands on the overlay, not the list behind it")
}

func TestPastingFillsTheOverlayFilter(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	openOverlay(t, app, "")

	send(t, app, tea.PasteMsg{Content: "unwatch"})
	assert.Equal(t, "unwatch", app.overlay.input.Value())
	assert.Equal(t, "watch / unwatch", firstTitle(t, app))
}

func TestTheShortcutOverlayFitsEveryTerminalSize(t *testing.T) {
	sizes := []struct{ width, height int }{
		{40, 12}, {60, 20}, {79, 24}, {80, 24}, {100, 30}, {200, 60}, {120, 8}, {45, 30}, {20, 6},
	}
	for _, size := range sizes {
		for _, query := range []string{"", "ch", "zzzz"} {
			t.Run(fmt.Sprintf("%dx%d %q", size.width, size.height, query), func(t *testing.T) {
				app, _, _ := newTestApp(t, size.width, size.height)
				openOverlay(t, app, query)

				for step := 0; step < 2; step++ {
					rendered := lines(app)
					assert.Len(t, rendered, size.height, "the overlay keeps the screen exactly the terminal's height")
					for i, line := range rendered {
						assert.LessOrEqual(t, ansi.StringWidth(line), size.width,
							"line %d overflows the terminal: %q", i, line)
					}
					// Measure again from the bottom of the list, where the
					// scroll position is furthest from where it started.
					for i := 0; i < 30; i++ {
						send(t, app, press("down"))
					}
				}
			})
		}
	}
}

// overlayBox returns the overlay's own lines, frame included, from a rendered
// screen.
func overlayBox(t *testing.T, app *App) []string {
	t.Helper()
	var box []string
	for _, line := range strings.Split(plain(app.render()), "\n") {
		if strings.ContainsAny(line, "╭╮╰╯│") {
			box = append(box, strings.TrimRight(line, " "))
		}
	}
	require.NotEmpty(t, box, "the overlay has to be on screen")
	return box
}

func TestTheOverlaySetsItsLastLineOffFromTheBottomEdge(t *testing.T) {
	// A width where the selected shortcut's explanation fills the detail
	// strip, which is when the text used to run straight into the bottom edge.
	// That edge carries its own hints, so the two read as one line.
	for _, size := range []struct {
		name          string
		width, height int
	}{
		{"with the detail strip", 62, 26},
		{"too short for the detail strip", 70, 12},
	} {
		t.Run(size.name, func(t *testing.T) {
			app, _, _ := newTestApp(t, size.width, size.height)
			send(t, app, press("?"))

			box := overlayBox(t, app)
			last := box[len(box)-1]
			require.Contains(t, last, "╰", "the last line is the bottom edge")

			above := box[len(box)-2]
			assert.Equal(t, "", strings.TrimSpace(strings.Trim(above, "│ ")),
				"the line above the bottom edge is blank, so the two do not read as one")
		})
	}
}
