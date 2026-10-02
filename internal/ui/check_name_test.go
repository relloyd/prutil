package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/relloyd/prutil/internal/model"
)

func TestCheckNameRemainsVisibleAtNarrowWidths(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	check := model.Check{
		Name:        "Build and test integration suite",
		Workflow:    "CI / nightly matrix",
		Status:      model.StatusFailure,
		StartedAt:   testNow.Add(-time.Minute),
		CompletedAt: testNow,
	}
	tests := []struct {
		name         string
		width        int
		wantName     string
		wantWorkflow bool
		wantDuration bool
	}{
		{"wide pane shows the complete name and workflow", 80, "Build and test integration suite", true, true},
		{"medium pane drops workflow and abbreviates the name", 40, "Build and test integration sui…", false, true},
		{"narrow pane still shows the start of the name", 20, "Build and …", false, true},
		{"very narrow pane drops duration to keep the name", 11, "Build …", false, false},
		{"last available name column shows a letter, not just an ellipsis", 5, "B", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line := ansi.Strip(app.renderCheck(check, tt.width, false))
			require.LessOrEqual(t, ansi.StringWidth(line), tt.width)
			assert.Contains(t, line, "✗ "+tt.wantName)
			assert.Equal(t, tt.wantWorkflow, strings.Contains(line, check.Workflow))
			assert.Equal(t, tt.wantDuration, strings.Contains(line, "1m0s"))
		})
	}
	short := check
	short.Name, short.Workflow = "Go", ""
	line := ansi.Strip(app.renderCheck(short, 11, false))
	assert.Contains(t, line, "✗ Go")
	assert.Contains(t, line, "1m0s", "short names should not lose a duration that fits")
}

func TestCheckNameRespondsToPaneResizing(t *testing.T) {
	app, _, _ := newTestApp(t, 120, 40)
	check := model.Check{
		Name:   "Build and test integration suite for every target",
		Status: model.StatusFailure,
	}
	send(t, app, checksMsg{gen: app.gen, key: samplePRs()[0].Key(), checks: []model.Check{check}})
	send(t, app, press("l"))

	for _, width := range []int{120, 80, 79, 120} {
		send(t, app, tea.WindowSizeMsg{Width: width, Height: 40})
		_, detailWidth := app.paneWidths()
		line := ansi.Strip(app.renderCheck(check, detailWidth, false))
		assert.Contains(t, line, "Build", "width %d must show the name", width)
		assert.LessOrEqual(t, ansi.StringWidth(line), detailWidth)
		assert.Contains(t, plain(app.render()), "Build", "width %d must show the name on screen", width)
		switch width {
		case 120:
			assert.Contains(t, line, check.Name, "widening must restore the complete name")
		case 80:
			assert.NotContains(t, line, check.Name, "the split pane must shorten the name")
		}
	}
}
