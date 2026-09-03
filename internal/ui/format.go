package ui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/relloyd/prutil/internal/model"
)

// ellipsis is appended wherever text has to be cut short.
const ellipsis = "…"

// seg is a run of plain text with the style it should be rendered in. Layout
// works on the plain text so that widths are measured without escape codes,
// and styles are applied only at the very end.
type seg struct {
	text  string
	style lipgloss.Style
}

// renderSegs styles and concatenates segments.
func renderSegs(sep string, segs ...seg) string {
	var b strings.Builder
	for i, s := range segs {
		if i > 0 {
			b.WriteString(sep)
		}
		b.WriteString(s.style.Render(s.text))
	}
	return b.String()
}

// fitSegs renders as many whole segments as fit into width, dropping the ones
// that do not. The first segment is always kept, truncated if it has to be, so
// a very narrow pane still shows the most important field on every line.
func fitSegs(width int, sep string, segs ...seg) string {
	if width <= 0 || len(segs) == 0 {
		return ""
	}
	kept := make([]seg, 0, len(segs))
	used := 0
	for i, s := range segs {
		if s.text == "" {
			continue
		}
		cost := ansi.StringWidth(s.text)
		if len(kept) > 0 {
			cost += ansi.StringWidth(sep)
		}
		if used+cost > width {
			if i == 0 || len(kept) == 0 {
				kept = append(kept, seg{text: ansi.Truncate(s.text, width, ellipsis), style: s.style})
			}
			break
		}
		kept = append(kept, s)
		used += cost
	}
	return renderSegs(sep, kept...)
}

// justify puts left and right on one line of exactly width columns, dropping
// the right-hand text when there is no room for it.
func justify(width int, left, right string) string {
	if width <= 0 {
		return ""
	}
	lw, rw := ansi.StringWidth(left), ansi.StringWidth(right)
	if rw == 0 {
		return padTo(ansi.Truncate(left, width, ellipsis), width)
	}
	if lw+1+rw > width {
		if rw+1 >= width {
			return padTo(ansi.Truncate(left, width, ellipsis), width)
		}
		left = ansi.Truncate(left, width-rw-1, ellipsis)
		lw = ansi.StringWidth(left)
	}
	return left + strings.Repeat(" ", width-lw-rw) + right
}

// padTo pads a rendered string with spaces so that a pane keeps its width even
// when a line is short, which matters once panes sit side by side.
func padTo(s string, width int) string {
	gap := width - ansi.StringWidth(s)
	if gap <= 0 {
		return s
	}
	return s + strings.Repeat(" ", gap)
}

// wrapLines word-wraps plain text into at most maxLines lines of the given
// width, marking the last line with an ellipsis when text had to be dropped.
func wrapLines(text string, width, maxLines int) []string {
	if width <= 0 || maxLines <= 0 {
		return nil
	}
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return []string{""}
	}
	wrapped := strings.Split(ansi.Wrap(text, width, " -/_"), "\n")
	if len(wrapped) <= maxLines {
		return wrapped
	}
	kept := wrapped[:maxLines]
	kept[maxLines-1] = ansi.Truncate(kept[maxLines-1], width-1, "") + ellipsis
	return kept
}

// statusGlyph returns the single character that stands for a check state.
func statusGlyph(s model.Status) string {
	switch s {
	case model.StatusSuccess:
		return "✓"
	case model.StatusFailure:
		return "✗"
	case model.StatusPending:
		return "●"
	case model.StatusCancelled:
		return "⊘"
	case model.StatusSkipped:
		return "–"
	case model.StatusNeutral:
		return "◦"
	default:
		return "·"
	}
}

// statusStyle maps a check state onto its colour.
func (s Styles) statusStyle(status model.Status) lipgloss.Style {
	switch status {
	case model.StatusSuccess:
		return s.Success
	case model.StatusFailure:
		return s.Failure
	case model.StatusPending:
		return s.Pending
	default:
		return s.Neutral
	}
}

// dot renders the coloured status indicator shown against a pull request.
func (s Styles) dot(status model.Status) seg {
	glyph := "●"
	if status == model.StatusUnknown {
		glyph = "○"
	}
	return seg{text: glyph, style: s.statusStyle(status)}
}

// badges returns the DRAFT and CONFLICT markers for a pull request.
func (s Styles) badges(pr model.PullRequest) []seg {
	var out []seg
	if pr.IsDraft {
		out = append(out, seg{text: "DRAFT", style: s.BadgeDraft})
	}
	if pr.Mergeable == model.MergeConflicting {
		out = append(out, seg{text: "CONFLICT", style: s.BadgeConflict})
	}
	return out
}

// reviewBadge returns the review decision marker, or an empty segment when
// GitHub has no opinion about the pull request.
func (s Styles) reviewBadge(pr model.PullRequest) seg {
	text := pr.ReviewDecision.String()
	if text == "" {
		return seg{}
	}
	switch pr.ReviewDecision {
	case model.ReviewApproved:
		return seg{text: text, style: s.BadgeApproved}
	case model.ReviewChangesRequested:
		return seg{text: text, style: s.BadgeChanges}
	default:
		return seg{text: text, style: s.BadgeReview}
	}
}

// countsText renders the per-state check tally shown on a list row.
func countsText(c model.CheckCounts) string {
	if c.Total == 0 {
		return "no checks"
	}
	parts := make([]string, 0, 4)
	if c.Success > 0 {
		parts = append(parts, fmt.Sprintf("✓%d", c.Success))
	}
	if c.Failure > 0 {
		parts = append(parts, fmt.Sprintf("✗%d", c.Failure))
	}
	if c.Pending > 0 {
		parts = append(parts, fmt.Sprintf("●%d", c.Pending))
	}
	if c.Other > 0 {
		parts = append(parts, fmt.Sprintf("◦%d", c.Other))
	}
	return strings.Join(parts, " ")
}

// diffText renders the size of a pull request's diff.
func diffText(pr model.PullRequest) string {
	if pr.ChangedFiles == 0 && pr.Additions == 0 && pr.Deletions == 0 {
		return ""
	}
	files := "files"
	if pr.ChangedFiles == 1 {
		files = "file"
	}
	return fmt.Sprintf("+%d −%d · %d %s", pr.Additions, pr.Deletions, pr.ChangedFiles, files)
}

// clipLines trims or pads a block of lines to exactly height lines, so that
// side-by-side panes always line up.
func clipLines(lines []string, height, width int) []string {
	out := make([]string, 0, height)
	for i := 0; i < height; i++ {
		if i < len(lines) {
			out = append(out, padTo(lines[i], width))
			continue
		}
		out = append(out, strings.Repeat(" ", max(width, 0)))
	}
	return out
}

// lenOf reports the display width of a possibly styled string.
func lenOf(s string) int {
	return ansi.StringWidth(s)
}

// shorten cuts a string to width columns, marking the cut with an ellipsis.
func shorten(s string, width int) string {
	return ansi.Truncate(s, width, ellipsis)
}
