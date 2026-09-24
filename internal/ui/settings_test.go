package ui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/relloyd/prutil/internal/home"
	"github.com/relloyd/prutil/internal/model"
)

// openSettingsPane presses s and checks the pane came up.
func openSettingsPane(t *testing.T, app *App) {
	t.Helper()
	send(t, app, press("s"))
	require.True(t, app.settings.open, "s opens the settings")
}

// approvedRow is the settings pane's row for approval notifications, as drawn.
func approvedRow(t *testing.T, app *App) string {
	t.Helper()
	for _, line := range lines(app) {
		if strings.Contains(line, "Pull request approved") {
			return line
		}
	}
	require.Fail(t, "the settings pane shows no approval row")
	return ""
}

// selfReviewRow is the settings pane's row for self-review feedback, as drawn.
func selfReviewRow(t *testing.T, app *App) string {
	t.Helper()
	for _, line := range lines(app) {
		if strings.Contains(line, "Self-review feedback") {
			return line
		}
	}
	require.Fail(t, "the settings pane shows no self-review row")
	return ""
}

func TestEveryNotificationHasOneEntryInTheSettings(t *testing.T) {
	events := make([]home.NotificationEvent, 0, len(notifications))
	for _, n := range notifications {
		assert.NotEmpty(t, n.setting)
		assert.NotEmpty(t, n.detail)
		assert.NotEmpty(t, n.headline)
		assert.NotNil(t, n.fired)
		events = append(events, n.event)
	}
	assert.Equal(t, home.NotificationEvents(), events,
		"the settings list every notification home knows about, once and in the same order")
}

func TestTheSettingsPaneShowsEveryNotificationWordForWord(t *testing.T) {
	// Asked of allSettings this could not fail, because the notification rows
	// are built by copying these very strings out of notifications. The pane
	// as drawn is the thing worth pinning: a notification that never reaches a
	// row is one the reader cannot reach either.
	app, _, _ := newTestApp(t, 120, 40)
	openSettingsPane(t, app)
	screen := plain(app.render())

	for _, n := range notifications {
		assert.Contains(t, screen, n.setting,
			"the settings pane draws no row for the %q notification", n.setting)
	}
	// The selected row's explanation is drawn beneath the list, and it is the
	// notification's own words that belong there, wrapped to the box.
	l := app.settingsLayout()
	for _, line := range wrapLines(notifications[app.settings.cursor].detail, l.inner, settingsDetailLines) {
		assert.Contains(t, screen, line,
			"the pane explains the selected notification in words of its own")
	}
}

func TestSOpensTheSettingsAndEscClosesThem(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	focus, cursor := app.focus, app.cur().cursor

	openSettingsPane(t, app)
	screen := plain(app.render())
	assert.Contains(t, screen, "Settings")
	assert.Contains(t, screen, "DESKTOP NOTIFICATIONS")
	assert.Contains(t, screen, "WATCHING")
	row := approvedRow(t, app)
	assert.Contains(t, row, "[✓]")
	assert.Contains(t, row, "on")
	srow := selfReviewRow(t, app)
	assert.Contains(t, srow, "[ ]")
	assert.Contains(t, srow, "off")
	assert.Contains(t, screen, "space toggle")
	assert.Contains(t, screen, "t test notification")

	send(t, app, press("esc"))
	assert.False(t, app.settings.open)
	assert.Equal(t, focus, app.focus, "esc closes the pane rather than going back a pane")
	assert.Equal(t, cursor, app.cur().cursor)
	assert.NotContains(t, plain(app.render()), "DESKTOP NOTIFICATIONS")
}

func TestTheKeysThatOpenTheSettingsAlsoCloseThem(t *testing.T) {
	for _, k := range []string{"s", ",", "q"} {
		t.Run(k, func(t *testing.T) {
			app, _, _ := newTestApp(t, 120, 40)
			send(t, app, press(","))
			require.True(t, app.settings.open, ", opens the settings too")

			msgs := drain(send(t, app, press(k)))
			assert.False(t, app.settings.open)
			assert.NotContains(t, msgs, tea.QuitMsg{}, "q closes the pane rather than quitting")
		})
	}
}

func TestCtrlCStillQuitsFromTheSettings(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	openSettingsPane(t, app)
	assert.Contains(t, drain(send(t, app, press("ctrl+c"))), tea.QuitMsg{})
}

func TestKeysDoNotReachTheAppWhileTheSettingsAreOpen(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	gen, active, cursor, focus := app.gen, app.active, app.cur().cursor, app.focus
	openSettingsPane(t, app)

	for _, k := range []string{"r", "j", "G", "a", "w", "l", "tab", "right", "?", "W", "y"} {
		send(t, app, press(k))
	}
	send(t, app, tea.PasteMsg{Content: "rjw"})

	assert.True(t, app.settings.open)
	assert.False(t, app.overlay.open, "? does not open the shortcuts over the settings")
	assert.Equal(t, gen, app.gen, "r did not refresh")
	assert.Equal(t, active, app.active, "tab did not switch the view")
	assert.Equal(t, cursor, app.cur().cursor, "j and G did not move the list")
	assert.Equal(t, focus, app.focus, "l and → did not focus the detail pane")
	assert.Zero(t, app.autoLeft, "a did not start auto-refresh")
	assert.False(t, app.armed(app.selectedKey()), "w did not watch anything")
	assert.Empty(t, clipboardOf(t, app).copied(), "y did not copy")
}

func TestTheShortcutOverlayOpensTheSettings(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	openOverlay(t, app, "settings")
	assert.Equal(t, "settings", firstTitle(t, app))

	send(t, app, press("enter"))
	assert.False(t, app.overlay.open)
	assert.True(t, app.settings.open, "enter on settings opens them")
}

func TestSpaceTurnsANotificationOffAndSavesIt(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	openSettingsPane(t, app)

	send(t, app, press("space"))
	assert.False(t, app.homeCfg.Notifications.Enabled(home.NotifyApproved))
	row := approvedRow(t, app)
	assert.Contains(t, row, "[ ]")
	assert.Contains(t, row, "off")
	assert.Contains(t, plain(app.render()), "Pull request approved is off · saved")

	saved, err := app.store.LoadConfig()
	require.NoError(t, err)
	assert.False(t, saved.Notifications.Enabled(home.NotifyApproved), "the next run starts with it off")

	send(t, app, press("enter"))
	assert.True(t, app.homeCfg.Notifications.Enabled(home.NotifyApproved), "enter toggles as well")
	send(t, app, press("x"))
	assert.False(t, app.homeCfg.Notifications.Enabled(home.NotifyApproved), "and so does x")
	saved, err = app.store.LoadConfig()
	require.NoError(t, err)
	assert.False(t, saved.Notifications.Enabled(home.NotifyApproved))
}

func TestSpaceTurnsSelfReviewOnAndSavesIt(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	openSettingsPane(t, app)

	send(t, app, press("tab"))
	assert.Equal(t, 2, app.settings.cursor)
	assert.Contains(t, plain(app.render()), "Treat every unresolved review comment")

	send(t, app, press("space"))
	assert.True(t, app.homeCfg.Watch.SelfReview)
	row := selfReviewRow(t, app)
	assert.Contains(t, row, "[✓]")
	assert.Contains(t, row, "on")
	assert.Contains(t, plain(app.render()), "Self-review feedback is on · saved")

	saved, err := app.store.LoadConfig()
	require.NoError(t, err)
	assert.True(t, saved.Watch.SelfReview, "the next run starts with it on")

	send(t, app, press("enter"))
	assert.False(t, app.homeCfg.Watch.SelfReview, "enter toggles as well")
	saved, err = app.store.LoadConfig()
	require.NoError(t, err)
	assert.False(t, saved.Watch.SelfReview)
}

func TestTogglingSelfReviewReadsTheWatchedPullRequestsAgain(t *testing.T) {
	// Nothing on GitHub changes when this setting does, so no tripwire will
	// ever ask about it: without a read of its own the counts on screen keep
	// answering the question as it was asked before.
	app, client, _ := newTestApp(t, 120, 40)
	send(t, app, press("w"))
	require.True(t, app.armed(model.Key{Repo: "relloyd/prutil", Number: 42}))
	before := client.reviewCalls

	openSettingsPane(t, app)
	send(t, app, press("tab"))
	pump(t, app, send(t, app, press("space")))

	assert.Equal(t, before+1, client.reviewCalls, "the armed pull request is read again")

	// Turning it off asks again, because the count it leaves behind was
	// computed under the rule that has just been dropped.
	pump(t, app, send(t, app, press("space")))
	assert.Equal(t, before+2, client.reviewCalls)
}

func TestTogglingANotificationLeavesTheWatcherAlone(t *testing.T) {
	// Only the settings that change what feedback means are worth a request.
	app, client, _ := newTestApp(t, 120, 40)
	send(t, app, press("w"))
	before := client.reviewCalls

	openSettingsPane(t, app)
	pump(t, app, send(t, app, press("space")))

	assert.Equal(t, before, client.reviewCalls)
}

func TestTheSettingsDoNotWriteIntoTheCallersConfiguration(t *testing.T) {
	cfg := fastWatch()
	app := New(Config{Client: newFakeClient(nil, nil), Home: cfg, Store: home.OpenIn(t.TempDir()), Notifier: &fakeNotifier{}})
	send(t, app, tea.WindowSizeMsg{Width: 120, Height: 40})
	openSettingsPane(t, app)
	send(t, app, press("space"))

	assert.False(t, app.homeCfg.Notifications.Enabled(home.NotifyApproved))
	assert.True(t, cfg.Notifications.Enabled(home.NotifyApproved), "the configuration main loaded is not the app's to change")
}

func TestASettingThatCannotBeSavedStillAppliesAndSaysSo(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	require.NoError(t, os.WriteFile(app.store.Path(home.ConfigFile), []byte("herdr: [\n"), 0o600))
	openSettingsPane(t, app)

	send(t, app, press("space"))
	assert.False(t, app.homeCfg.Notifications.Enabled(home.NotifyApproved), "the change applies for this run")
	assert.True(t, app.settings.noticeErr)
	assert.Contains(t, app.settings.notice, "is off until prutil quits, but was not saved")
	assert.Contains(t, app.settings.notice, "could not read the configuration")

	data, err := os.ReadFile(app.store.Path(home.ConfigFile))
	require.NoError(t, err)
	assert.Equal(t, "herdr: [\n", string(data), "the file that does not parse is left as it was")
}

func TestWithoutAnApplicationDirectoryASettingLastsTheSession(t *testing.T) {
	app := New(Config{
		Client:   newFakeClient(samplePRs(), sampleChecks()),
		Home:     fastWatch(),
		StoreErr: errors.New("no home directory"),
		Notifier: &fakeNotifier{},
	})
	send(t, app, tea.WindowSizeMsg{Width: 120, Height: 40})
	openSettingsPane(t, app)
	assert.Contains(t, plain(app.render()), "not saved", "the frame says there is nowhere to save to")

	send(t, app, press("space"))
	assert.False(t, app.homeCfg.Notifications.Enabled(home.NotifyApproved))
	assert.Contains(t, app.settings.notice, "not saved: no home directory")
}

func TestTheSettingsSayWhenNothingWouldAppear(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	notifierOf(t, app).unavailable = errors.New("desktop notifications need notify-send, which is not installed")

	openSettingsPane(t, app)
	assert.Contains(t, plain(app.render()), "need notify-send")

	send(t, app, press("space"))
	send(t, app, press("space"))
	assert.True(t, app.settings.noticeErr, "turning it on says it will not help")
	assert.Contains(t, app.settings.notice, "is on and saved, but nothing will appear: desktop notifications need notify-send")
}

func TestWithoutANotifierTheSettingsSayNotificationsCannotBeShown(t *testing.T) {
	app := New(Config{Client: newFakeClient(samplePRs(), nil), Home: fastWatch(), Store: home.OpenIn(t.TempDir())})
	send(t, app, tea.WindowSizeMsg{Width: 120, Height: 40})
	openSettingsPane(t, app)

	assert.Contains(t, plain(app.render()), "prutil cannot show desktop notifications here")
	assert.Nil(t, send(t, app, press("t")))
	assert.True(t, app.settings.noticeErr)
}

func TestTSendsATestNotification(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	notifierOf(t, app).hint = "Allow notifications from Script Editor."
	openSettingsPane(t, app)

	cmd := send(t, app, press("t"))
	assert.Contains(t, app.settings.notice, "sending a test notification")
	for _, msg := range drain(cmd) {
		send(t, app, msg)
	}

	shown := notifierOf(t, app).notifications()
	require.Len(t, shown, 1)
	assert.Equal(t, "Test notification", shown[0].Title)
	assert.False(t, app.settings.noticeErr)
	assert.Equal(t, "sent a test notification. Nothing appeared? Allow notifications from Script Editor.", app.settings.notice)
	assert.Contains(t, plain(app.render()), "Nothing appeared?")
}

func TestAFailedTestNotificationIsReported(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	notifierOf(t, app).err = errors.New("osascript: not allowed")
	openSettingsPane(t, app)

	for _, msg := range drain(send(t, app, press("t"))) {
		send(t, app, msg)
	}
	assert.True(t, app.settings.noticeErr)
	assert.Equal(t, "the test notification failed: osascript: not allowed", app.settings.notice)
}

func TestATestNotificationThatFinishesAfterThePaneClosedGoesToTheStatusLine(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	openSettingsPane(t, app)
	cmd := send(t, app, press("t"))
	send(t, app, press("esc"))

	var reply tea.Cmd
	for _, msg := range drain(cmd) {
		reply = send(t, app, msg)
	}
	require.NotNil(t, reply)
	assert.Equal(t, statusMsg("sent a test notification"), reply())
}

func TestClickingASettingTogglesItAndClicksElsewhereDoNothing(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	cursor := app.cur().cursor
	openSettingsPane(t, app)
	l := app.settingsLayout()

	send(t, app, click(0, headerHeight+rowHeight))
	assert.True(t, app.homeCfg.Notifications.Enabled(home.NotifyApproved), "a click outside the pane changes nothing")
	assert.Equal(t, cursor, app.cur().cursor, "and does not reach the list behind it")
	assert.True(t, app.settings.open)

	send(t, app, click(l.x+4, l.y+1))
	assert.True(t, app.homeCfg.Notifications.Enabled(home.NotifyApproved), "a click on the heading changes nothing")

	send(t, app, click(l.x+4, l.y+2))
	assert.False(t, app.homeCfg.Notifications.Enabled(home.NotifyApproved), "a click on the row toggles it")
	send(t, app, tea.MouseClickMsg{X: l.x + 4, Y: l.y + 2, Button: tea.MouseRight})
	assert.False(t, app.homeCfg.Notifications.Enabled(home.NotifyApproved), "only the left button toggles")

	send(t, app, click(l.x+4, l.y+4))
	assert.False(t, app.homeCfg.Watch.SelfReview, "a click on the second heading changes nothing")

	send(t, app, click(l.x+4, l.y+5))
	assert.True(t, app.homeCfg.Watch.SelfReview, "a click on the self-review row toggles it")

	send(t, app, tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	assert.Equal(t, cursor, app.cur().cursor, "the wheel does not scroll the list behind the pane")
}

func TestMovingTheSelectionStaysInsideTheListAndClearsTheNotice(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	openSettingsPane(t, app)
	send(t, app, press("space"))
	require.NotEmpty(t, app.settings.notice)

	send(t, app, press("k"))
	assert.Zero(t, app.settings.cursor, "up at the top stays put")
	assert.NotEmpty(t, app.settings.notice, "staying put keeps the notice")
	send(t, app, press("j"))
	assert.Equal(t, 1, app.settings.cursor)
	assert.Empty(t, app.settings.notice, "a notice about another row is cleared")
	send(t, app, press("G"))
	assert.Equal(t, len(allSettings())-1, app.settings.cursor)
	send(t, app, press("j"))
	assert.Equal(t, len(allSettings())-1, app.settings.cursor, "down at the bottom stays put")
}

func TestTheSettingsExplainTheSelectedNotificationAndHowOftenPrutilLooks(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	app.homeCfg.Notifications.Interval = home.Duration(2 * time.Minute)
	openSettingsPane(t, app)

	screen := plain(app.render())
	assert.Contains(t, screen, "When one of your open pull requests is approved")
	assert.Contains(t, screen, "every 2 minutes")
}

func TestTheSettingsReportAFailedCheckForChanges(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	send(t, app, notifyPollMsg{err: errors.New("could not resolve host")})
	openSettingsPane(t, app)

	assert.Contains(t, plain(app.render()), "the last check for changes failed: could not resolve host")
}

func TestHumanInterval(t *testing.T) {
	cases := map[string]string{
		"1m0s":  "minute",
		"2m0s":  "2 minutes",
		"30s":   "30 seconds",
		"1m30s": "1m30s",
	}
	for in, want := range cases {
		d, err := time.ParseDuration(in)
		require.NoError(t, err)
		assert.Equal(t, want, humanInterval(d), in)
	}
}

func TestTheSettingsFitEveryTerminalSize(t *testing.T) {
	sizes := []struct{ width, height int }{
		{40, 12}, {60, 20}, {79, 24}, {80, 24}, {100, 30}, {200, 60}, {120, 8}, {45, 30}, {20, 6},
	}
	for _, size := range sizes {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			app, _, _ := newTestApp(t, size.width, size.height)
			notifierOf(t, app).unavailable = errors.New("desktop notifications need notify-send, which is not installed (it comes with libnotify)")
			openSettingsPane(t, app)

			for step := 0; step < 2; step++ {
				rendered := lines(app)
				assert.LessOrEqual(t, len(rendered), size.height, "the pane fits inside the window")
				for _, l := range rendered {
					assert.LessOrEqual(t, ansi.StringWidth(l), size.width, "no line extends past the right edge")
				}
				send(t, app, press("j"))
			}
		})
	}
}

func TestSettingsSteppingAndCycling(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	app.homeCfg.Notifications.Interval = home.Duration(2 * time.Minute)
	openSettingsPane(t, app)

	// Cursor starts on notifications.approved (item 0)
	assert.Equal(t, 0, app.settings.cursor)

	// Move down to notifications.interval (item 1)
	send(t, app, press("j"))
	assert.Equal(t, 1, app.settings.cursor)

	// Step interval up with +
	send(t, app, press("+"))
	assert.Equal(t, home.Duration(2*time.Minute+30*time.Second), app.homeCfg.Notifications.Interval)
	assert.Contains(t, app.settings.notice, "Check poll interval set to 2m30s · saved")

	// Step interval down with -
	send(t, app, press("-"))
	assert.Equal(t, home.Duration(2*time.Minute), app.homeCfg.Notifications.Interval)

	// Jump to next section with tab (WATCHING & POLLING)
	send(t, app, press("tab"))
	assert.Equal(t, 2, app.settings.cursor) // watch.self_review

	// Jump to next section with tab (AI REVIEW TRIGGER)
	send(t, app, press("tab"))
	assert.Equal(t, 12, app.settings.cursor) // review.comment

	// Jump to next section with tab (CODING AGENT)
	send(t, app, press("tab"))
	assert.Equal(t, 14, app.settings.cursor) // herdr.fallback

	// Cycle fallback strategy
	assert.Equal(t, home.FallbackNew, app.homeCfg.Herdr.Fallback)
	send(t, app, press("right"))
	assert.Equal(t, home.FallbackNone, app.homeCfg.Herdr.Fallback)
	assert.Contains(t, app.settings.notice, "Fallback strategy set to \"none\" · saved")

	send(t, app, press("right"))
	assert.Equal(t, home.FallbackRepo, app.homeCfg.Herdr.Fallback)

	send(t, app, press("d")) // Reset to default
	assert.Equal(t, home.FallbackNew, app.homeCfg.Herdr.Fallback)
	assert.Contains(t, app.settings.notice, "reset to default · saved")
}

func TestSettingsInlineTextEditing(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	openSettingsPane(t, app)

	// Jump to review.comment (item 12)
	send(t, app, press("tab"))
	send(t, app, press("tab"))
	assert.Equal(t, 12, app.settings.cursor)

	// Press enter to edit
	send(t, app, press("enter"))
	assert.Equal(t, settingsModeEdit, app.settings.mode)

	// Clear and enter new comment
	app.settings.input.SetValue("/claude review")
	send(t, app, press("enter"))
	assert.Equal(t, settingsModeNormal, app.settings.mode)
	assert.Equal(t, "/claude review", app.homeCfg.Review.CommentFor(""))
	assert.Contains(t, app.settings.notice, "Review comment set to \"/claude review\" · saved")
}

func TestSettingsSubPaneMapAndSequence(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	openSettingsPane(t, app)

	cursorOn(t, app, "discovery.roots")

	// Open sub-pane
	send(t, app, press("enter"))
	assert.Equal(t, settingsModeSubPane, app.settings.mode)
	assert.Equal(t, subPaneDiscoveryRoots, app.settings.subPane.kind)

	// Add a root
	send(t, app, press("a"))
	assert.True(t, app.settings.subPane.adding)
	app.settings.subPane.valInput.SetValue("~/src")
	send(t, app, press("enter"))
	assert.False(t, app.settings.subPane.adding)
	assert.Equal(t, []string{"~/src"}, app.homeCfg.Discovery.Roots)
	assert.Contains(t, app.settings.notice, "Added discovery root \"~/src\" · saved")

	// Delete the root
	send(t, app, press("d"))
	assert.Empty(t, app.homeCfg.Discovery.Roots)
	assert.Contains(t, app.settings.notice, "Deleted discovery root \"~/src\" · saved")

	// Close sub-pane with esc
	send(t, app, press("esc"))
	assert.Equal(t, settingsModeNormal, app.settings.mode)
}

func TestSettingsTemplateViewerAndEditor(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	openSettingsPane(t, app)

	promptIdx := -1
	for i, it := range allSettings() {
		if it.id == "herdr.prompt" {
			promptIdx = i
			break
		}
	}
	require.True(t, promptIdx >= 0)
	app.settings.cursor = promptIdx

	// Press enter to view template modal
	send(t, app, press("enter"))
	assert.Equal(t, settingsModeTemplate, app.settings.mode)

	// Render screen in template mode
	screen := plain(app.render())
	assert.Contains(t, screen, "Template: Review handoff prompt")
	assert.Contains(t, screen, "1 │")
	assert.Contains(t, screen, "edit in $EDITOR")

	// Scroll down and up
	send(t, app, press("down"))
	assert.Equal(t, 1, app.settings.templateScroll)
	send(t, app, press("up"))
	assert.Equal(t, 0, app.settings.templateScroll)

	// Simulate editor return with updated template
	tmpFile, err := os.CreateTemp("", "test-tmpl-*.tmpl")
	require.NoError(t, err)
	defer func() { _ = os.Remove(tmpFile.Name()) }()
	_, err = tmpFile.WriteString("Custom template for {{.Repo}}#{{.Number}}: {{.URL}}\n" + model.AgentCommentMarker + "\n")
	require.NoError(t, err)
	_ = tmpFile.Close()

	cmd := app.handleTemplateEditorFinished(templateEditorFinishedMsg{
		tmpFile: tmpFile.Name(),
		isCheck: false,
		err:     nil,
	})
	assert.Nil(t, cmd)
	assert.Contains(t, app.homeCfg.Herdr.Prompt, "Custom template for")
	assert.Contains(t, app.settings.notice, "prompt template updated · saved")

	// Render screen again to verify new template content is visible
	screenAfter := plain(app.render())
	assert.Contains(t, screenAfter, "Custom template for")

	// Reset to default
	send(t, app, press("d"))
	assert.Equal(t, home.DefaultPrompt, app.homeCfg.Herdr.Prompt)
	assert.Contains(t, app.settings.notice, "template reset to default · saved")

	// Press esc to return to normal list
	send(t, app, press("esc"))
	assert.Equal(t, settingsModeNormal, app.settings.mode)
}

func TestSettingsSubPaneRendering(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	openSettingsPane(t, app)

	cursorOn(t, app, "discovery.roots")

	// Enter subpane
	send(t, app, press("enter"))
	assert.Equal(t, settingsModeSubPane, app.settings.mode)

	// Render empty subpane screen
	screen := plain(app.render())
	assert.Contains(t, screen, "Discovery: Checkout Roots")
	assert.Contains(t, screen, "no entries configured")

	// Start adding
	send(t, app, press("a"))
	assert.True(t, app.settings.subPane.adding)
	addScreen := plain(app.render())
	assert.Contains(t, addScreen, "ADD NEW ENTRY")
	assert.Contains(t, addScreen, "Discovery root:")

	// Cancel adding
	send(t, app, press("esc"))
	assert.False(t, app.settings.subPane.adding)
	assert.Equal(t, settingsModeSubPane, app.settings.mode)

	// Return to normal mode
	send(t, app, press("esc"))
	assert.Equal(t, settingsModeNormal, app.settings.mode)
}

func TestAFailedSaveLeavesTheRunningConfigurationAlone(t *testing.T) {
	dir := t.TempDir()
	// A root written as a flow mapping is a configuration prutil will not edit,
	// so every save refuses. Any other refusal would do; what matters is that
	// the write fails after the reader has asked for the change.
	require.NoError(t, os.WriteFile(filepath.Join(dir, home.ConfigFile), []byte("{watch: {base_interval: 2m}}\n"), 0o600))

	cfg := fastWatch()
	cfg.Watch.SelfReview = false
	app := New(Config{Client: newFakeClient(nil, nil), Home: cfg, Store: home.OpenIn(dir), Notifier: &fakeNotifier{}})
	send(t, app, tea.WindowSizeMsg{Width: 120, Height: 40})

	err := app.saveSetting([]string{"watch", "base_interval"}, "9m", func(c *home.Config) {
		c.Watch.BaseInterval = home.Duration(9 * time.Minute)
	})

	require.Error(t, err, "the file cannot be edited, so the save has to fail")
	assert.Equal(t, cfg.Watch.BaseInterval, app.homeCfg.Watch.BaseInterval,
		"a value that is not in the file is not the value prutil polls by")
}

func TestASucceedingSaveMovesTheRunningConfigurationWithIt(t *testing.T) {
	app := New(Config{Client: newFakeClient(nil, nil), Home: fastWatch(), Store: home.OpenIn(t.TempDir()), Notifier: &fakeNotifier{}})
	send(t, app, tea.WindowSizeMsg{Width: 120, Height: 40})

	require.NoError(t, app.saveSetting([]string{"watch", "base_interval"}, "9m", func(c *home.Config) {
		c.Watch.BaseInterval = home.Duration(9 * time.Minute)
	}))

	assert.Equal(t, home.Duration(9*time.Minute), app.homeCfg.Watch.BaseInterval)
	saved, err := home.OpenIn(app.store.Dir()).LoadOrCreateConfig()
	require.NoError(t, err)
	assert.Equal(t, home.Duration(9*time.Minute), saved.Watch.BaseInterval, "and the file agrees")
}

// settingsApp is an app on a default configuration with a real store behind it,
// which is what the registry's own closures need: they save as they go.
func settingsApp(t *testing.T) *App {
	t.Helper()
	app := New(Config{
		Client:   newFakeClient(nil, nil),
		Home:     home.DefaultConfig(),
		Store:    home.OpenIn(t.TempDir()),
		Notifier: &fakeNotifier{},
	})
	send(t, app, tea.WindowSizeMsg{Width: 120, Height: 40})
	return app
}

// TestEverySettingAnswersItsOwnControls holds the registry to the behaviour it
// has, one case per setting, so that changing how descriptors are built cannot
// quietly change what any of them does.
//
// The properties are the ones every kind shares: a fresh configuration reads as
// the default, whatever getRaw offers is something saveInput accepts, a change
// is visible and a reset undoes it, and at every point the configuration prutil
// is running on says the same as the file on disk. That last one is the
// property the pane had been getting wrong.
func TestEverySettingAnswersItsOwnControls(t *testing.T) {
	for _, d := range allSettings() {
		t.Run(d.id, func(t *testing.T) {
			app := settingsApp(t)

			require.NotEmpty(t, d.title, "a row needs something to call itself")
			require.NotEmpty(t, d.section, "and a section to sit in")
			// Every setting shows a value, booleans included. watch.self_review
			// used not to, which is what going through a constructor fixed.
			require.NotNil(t, d.getDisplay, "and something to show")
			assert.NotPanics(t, func() { _ = d.getDisplay(app) })
			if d.isDefault != nil {
				assert.True(t, d.isDefault(app), "a default configuration is the default")
			}

			// Whatever the row offers for editing has to be something it will
			// take back, unchanged.
			if d.saveInput != nil && d.getRaw != nil {
				before := d.getDisplay(app)
				require.NoError(t, d.saveInput(app, d.getRaw(app)), "its own value is valid input")
				assert.Equal(t, before, d.getDisplay(app), "and saving it changes nothing")
			}

			changed := true
			switch {
			case d.toggle != nil:
				d.toggle(app)
			case d.step != nil:
				d.step(app, 1)
			case d.cycle != nil:
				d.cycle(app, 1)
			default:
				changed = false
			}

			if changed && d.isDefault != nil {
				assert.False(t, d.isDefault(app), "the control moved it off the default")
			}
			if d.reset != nil {
				require.NoError(t, d.reset(app))
				if d.isDefault != nil {
					assert.True(t, d.isDefault(app), "and the reset put it back")
				}
			}

			// The file is the other half of every one of those operations.
			saved, err := home.OpenIn(app.store.Dir()).LoadOrCreateConfig()
			require.NoError(t, err)
			onDisk := withConfig(app, saved)
			assert.Equal(t, d.getDisplay(app), d.getDisplay(onDisk),
				"what prutil is running on is what the file says")
		})
	}
}

// withConfig returns the app reading a different configuration, so a descriptor
// can be asked what it would show for the file's version of the same setting.
func withConfig(app *App, cfg home.Config) *App {
	clone := *app
	clone.homeCfg = cfg
	return &clone
}

// cursorOn puts the settings cursor on one setting by id.
func cursorOn(t *testing.T, app *App, id string) {
	t.Helper()
	for i, d := range allSettings() {
		if d.id == id {
			app.settings.cursor = i
			return
		}
	}
	t.Fatalf("no setting called %q", id)
}

func TestEverySettingsPaneSetsItsLastLineOffFromTheBottomEdge(t *testing.T) {
	// The notice sits at the foot of all three, and the bottom edge carries
	// its own hints, so without a blank line between them the two read as one.
	open := map[string]func(t *testing.T, app *App){
		"the settings list": func(*testing.T, *App) {},
		"the template modal": func(t *testing.T, app *App) {
			cursorOn(t, app, "herdr.prompt")
			send(t, app, press("enter"))
			require.Equal(t, settingsModeTemplate, app.settings.mode)
		},
		"a collection sub-pane": func(t *testing.T, app *App) {
			cursorOn(t, app, "discovery.roots")
			send(t, app, press("enter"))
			require.Equal(t, settingsModeSubPane, app.settings.mode)
		},
	}
	for name, enter := range open {
		t.Run(name, func(t *testing.T) {
			for _, height := range []int{40, 24} {
				app, _, _ := newTestApp(t, 100, height)
				openSettingsPane(t, app)
				enter(t, app)

				box := overlayBox(t, app)
				last := box[len(box)-1]
				require.Contains(t, last, "╰", "the last line is the bottom edge, at height %d", height)
				above := box[len(box)-2]
				assert.Equal(t, "", strings.TrimSpace(strings.Trim(above, "│ ")),
					"the line above the bottom edge is blank, at height %d", height)
			}
		})
	}
}

func TestTheSettingsPaneKeepsItsExplanationWhenThereIsRoomForOne(t *testing.T) {
	// The blank line above the edge is taken from the list, which scrolls, and
	// not from the explanation, which does not exist anywhere else on screen.
	app, _, _ := newTestApp(t, 120, 40)
	openSettingsPane(t, app)
	cursorOn(t, app, "watch.self_review")

	assert.Contains(t, plain(app.render()), "Treat every unresolved review comment",
		"the selected setting still explains itself")
}

func TestTheTrustListsAreManagedFromTheSettingsPane(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	openSettingsPane(t, app)
	cursorOn(t, app, "security.trusted_authors")

	send(t, app, press("enter"))
	require.Equal(t, settingsModeSubPane, app.settings.mode)
	assert.Equal(t, subPaneTrustedAuthors, app.settings.subPane.kind)
	assert.Equal(t, []string{"gemini-code-assist[bot]"}, app.subPaneEntries(),
		"the shipped default is what the pane opens on")

	send(t, app, press("a"))
	app.settings.subPane.valInput.SetValue("colleague")
	send(t, app, press("enter"))

	assert.Equal(t, []string{"gemini-code-assist[bot]", "colleague"}, app.homeCfg.Security.TrustedAuthors)
	assert.Contains(t, app.settings.notice, `Added trusted author "colleague" · saved`)

	send(t, app, press("d"))
	assert.Equal(t, []string{"colleague"}, app.homeCfg.Security.TrustedAuthors,
		"the cursor was on the first entry, so that is the one removed")
}

func TestTrustedAssociationsAreCheckedAgainstWhatGitHubReports(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	openSettingsPane(t, app)
	cursorOn(t, app, "security.trusted_associations")
	send(t, app, press("enter"))
	require.Equal(t, subPaneTrustedAssociations, app.settings.subPane.kind)

	// A value GitHub never reports can only ever fail to match, so it is a
	// typo rather than a stricter policy, and saying so beats trusting nobody.
	send(t, app, press("a"))
	app.settings.subPane.valInput.SetValue("OWNRE")
	send(t, app, press("enter"))

	assert.Equal(t, []string{"OWNER", "COLLABORATOR"}, app.homeCfg.Security.TrustedAssociations,
		"nothing was added")
	assert.Contains(t, app.settings.notice, "is not a GitHub author association")
	assert.True(t, app.settings.noticeErr)

	// GitHub's values are upper case, so the reader need not shout.
	send(t, app, press("a"))
	app.settings.subPane.valInput.SetValue("member")
	send(t, app, press("enter"))

	assert.Equal(t, []string{"OWNER", "COLLABORATOR", "MEMBER"}, app.homeCfg.Security.TrustedAssociations)
}

func TestATrustedEntryCannotBeListedTwice(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	openSettingsPane(t, app)
	cursorOn(t, app, "security.trusted_associations")
	send(t, app, press("enter"))

	send(t, app, press("a"))
	app.settings.subPane.valInput.SetValue("owner")
	send(t, app, press("enter"))

	assert.Equal(t, []string{"OWNER", "COLLABORATOR"}, app.homeCfg.Security.TrustedAssociations)
	assert.Contains(t, app.settings.notice, "is already listed")
}

func TestATrustedAuthorHasToLookLikeALogin(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	openSettingsPane(t, app)
	cursorOn(t, app, "security.trusted_authors")
	send(t, app, press("enter"))

	for _, bad := range []string{"not a login", "https://github.com/someone", "a@b"} {
		send(t, app, press("a"))
		app.settings.subPane.valInput.SetValue(bad)
		send(t, app, press("enter"))
		assert.Contains(t, app.settings.notice, "is not a GitHub login", bad)
	}

	assert.Equal(t, []string{"gemini-code-assist[bot]"}, app.homeCfg.Security.TrustedAuthors,
		"none of them was saved")

	send(t, app, press("a"))
	app.settings.subPane.valInput.SetValue("dependabot[bot]")
	send(t, app, press("enter"))
	assert.Contains(t, app.homeCfg.Security.TrustedAuthors, "dependabot[bot]",
		"the [bot] suffix is how an app is named, so it has to be accepted")
}

func TestEmptyingATrustListIsNotTheSameAsLeavingItAtItsDefault(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	openSettingsPane(t, app)

	descriptor := settingByID(t, "security.trusted_authors")
	require.True(t, descriptor.isDefault(app), "a fresh configuration is the default")

	cursorOn(t, app, "security.trusted_authors")
	send(t, app, press("enter"))
	send(t, app, press("d"))

	require.Empty(t, app.homeCfg.Security.TrustedAuthors)
	assert.False(t, descriptor.isDefault(app),
		"trusting nobody is a deliberate choice, not the shipped boundary")
}

// settingByID finds one registered setting, so a test can ask about it without
// depending on where it sits in the list.
func settingByID(t *testing.T, id string) settingDescriptor {
	t.Helper()
	for _, d := range allSettings() {
		if d.id == id {
			return d
		}
	}
	t.Fatalf("no setting registered as %q", id)
	return settingDescriptor{}
}
