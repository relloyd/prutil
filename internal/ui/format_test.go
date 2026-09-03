package ui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/relloyd/prutil/internal/model"
)

func TestJustifyFillsExactlyTheGivenWidth(t *testing.T) {
	got := justify(20, "left", "right")
	assert.Equal(t, 20, ansi.StringWidth(got))
	assert.True(t, strings.HasPrefix(got, "left"))
	assert.True(t, strings.HasSuffix(got, "right"))
}

func TestJustifyTruncatesTheLeftSideFirst(t *testing.T) {
	got := justify(12, "a very long left side", "2d")
	assert.LessOrEqual(t, ansi.StringWidth(got), 12)
	assert.True(t, strings.HasSuffix(got, "2d"), "the age stays visible")
	assert.Contains(t, got, ellipsis)
}

func TestJustifyDropsTheRightSideWhenThereIsNoRoom(t *testing.T) {
	got := justify(4, "left side", "a long right side")
	assert.Equal(t, 4, ansi.StringWidth(got))
	assert.NotContains(t, got, "right")
}

func TestJustifyPadsShortLines(t *testing.T) {
	assert.Equal(t, 10, ansi.StringWidth(justify(10, "hi", "")))
	assert.Empty(t, justify(0, "hi", "there"))
}

func TestFitSegsDropsSegmentsThatDoNotFit(t *testing.T) {
	segs := []seg{
		{text: "first"},
		{text: "second"},
		{text: "third"},
	}

	assert.Equal(t, "first second third", plain(fitSegs(40, " ", segs...)))
	assert.Equal(t, "first second", plain(fitSegs(13, " ", segs...)), "the tail is dropped, not wrapped")
	assert.Equal(t, "first", plain(fitSegs(5, " ", segs...)))
}

func TestFitSegsTruncatesTheFirstSegmentWhenItAloneIsTooWide(t *testing.T) {
	got := plain(fitSegs(6, " ", seg{text: "an-extremely-long-branch-name"}))
	assert.Equal(t, 6, ansi.StringWidth(got))
	assert.True(t, strings.HasSuffix(got, ellipsis))
}

func TestFitSegsSkipsEmptySegments(t *testing.T) {
	got := plain(fitSegs(40, " · ", seg{text: "one"}, seg{}, seg{text: "two"}))
	assert.Equal(t, "one · two", got)
	assert.Empty(t, fitSegs(0, " ", seg{text: "one"}))
}

func TestFitSegsKeepsStyling(t *testing.T) {
	styled := fitSegs(40, " ", seg{text: "bold", style: lipgloss.NewStyle().Bold(true)})
	assert.NotEqual(t, "bold", styled, "styling is applied to the rendered output")
	assert.Equal(t, "bold", plain(styled))
}

func TestWrapLines(t *testing.T) {
	got := wrapLines("the quick brown fox jumps over the lazy dog", 12, 4)
	require.Greater(t, len(got), 1)
	for _, line := range got {
		assert.LessOrEqual(t, ansi.StringWidth(line), 12)
	}
	assert.Equal(t, "the quick", got[0])
}

func TestWrapLinesStopsAtTheLineBudget(t *testing.T) {
	got := wrapLines("the quick brown fox jumps over the lazy dog", 10, 2)
	require.Len(t, got, 2)
	assert.True(t, strings.HasSuffix(got[1], ellipsis), "dropped text is marked")
	assert.LessOrEqual(t, ansi.StringWidth(got[1]), 10)
}

func TestWrapLinesNormalisesWhitespace(t *testing.T) {
	assert.Equal(t, []string{"a b c"}, wrapLines("  a\n b   c ", 20, 2))
	assert.Equal(t, []string{""}, wrapLines("   ", 20, 2))
	assert.Nil(t, wrapLines("anything", 0, 2))
	assert.Nil(t, wrapLines("anything", 20, 0))
}

func TestClipLinesSquaresOffAPane(t *testing.T) {
	got := clipLines([]string{"one", "two"}, 4, 6)
	require.Len(t, got, 4)
	for _, line := range got {
		assert.Equal(t, 6, ansi.StringWidth(line), "every line is padded to the pane width")
	}

	assert.Len(t, clipLines([]string{"one", "two", "three"}, 2, 6), 2, "extra lines are cut")
}

func TestCountsText(t *testing.T) {
	assert.Equal(t, "no checks", countsText(model.CheckCounts{}))
	assert.Equal(t, "✓2 ✗1 ●3 ◦1", countsText(model.CheckCounts{Success: 2, Failure: 1, Pending: 3, Other: 1, Total: 7}))
	assert.Equal(t, "✓4", countsText(model.CheckCounts{Success: 4, Total: 4}))
}

func TestDiffText(t *testing.T) {
	assert.Empty(t, diffText(model.PullRequest{}))
	assert.Equal(t, "+1 −0 · 1 file", diffText(model.PullRequest{Additions: 1, ChangedFiles: 1}))
	assert.Equal(t, "+120 −30 · 7 files", diffText(model.PullRequest{Additions: 120, Deletions: 30, ChangedFiles: 7}))
}

func TestStatusGlyphs(t *testing.T) {
	cases := map[model.Status]string{
		model.StatusSuccess:   "✓",
		model.StatusFailure:   "✗",
		model.StatusPending:   "●",
		model.StatusCancelled: "⊘",
		model.StatusSkipped:   "–",
		model.StatusNeutral:   "◦",
		model.StatusUnknown:   "·",
	}
	for status, want := range cases {
		assert.Equal(t, want, statusGlyph(status), "glyph for %s", status)
	}
}

func TestBadges(t *testing.T) {
	styles := newStyles(true)

	assert.Empty(t, styles.badges(model.PullRequest{}))

	badges := styles.badges(model.PullRequest{IsDraft: true, Mergeable: model.MergeConflicting})
	require.Len(t, badges, 2)
	assert.Equal(t, "DRAFT", badges[0].text)
	assert.Equal(t, "CONFLICT", badges[1].text)

	assert.Empty(t, styles.reviewBadge(model.PullRequest{}).text)
	assert.Equal(t, "APPROVED", styles.reviewBadge(model.PullRequest{ReviewDecision: model.ReviewApproved}).text)
}

func TestTruncatePlain(t *testing.T) {
	assert.Equal(t, "short", truncatePlain("short", 10))
	assert.Equal(t, 5, ansi.StringWidth(truncatePlain("a-much-longer-branch", 5)))
	assert.Empty(t, truncatePlain("anything", 0))
}
