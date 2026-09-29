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
	// State is only set for a pull request that has been closed, and such a
	// pull request can never merge again, so its outcome replaces the
	// mergeable badge rather than sitting beside it.
	if text := pr.State.String(); text != "" {
		style := s.BadgeMerged
		if pr.State == model.PRStateClosed {
			style = s.BadgeClosed
		}
		return append(out, seg{text: text, style: style})
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

// checkCountSeg renders the per-state check tally shown on a list row, with
// each count in its own status colour — the same green/red/amber/grey the
// summary dot already uses — merged into one segment so it costs one slot in
// a row's width budget.
func (s Styles) checkCountSeg(c model.CheckCounts) seg {
	if c.Total == 0 {
		return seg{text: "no checks", style: s.Meta}
	}
	segs := make([]seg, 0, 4)
	if c.Success > 0 {
		segs = append(segs, seg{text: fmt.Sprintf("✓%d", c.Success), style: s.Success})
	}
	if c.Failure > 0 {
		segs = append(segs, seg{text: fmt.Sprintf("✗%d", c.Failure), style: s.Failure})
	}
	if c.Pending > 0 {
		segs = append(segs, seg{text: fmt.Sprintf("●%d", c.Pending), style: s.Pending})
	}
	if c.Other > 0 {
		segs = append(segs, seg{text: fmt.Sprintf("◦%d", c.Other), style: s.Neutral})
	}
	return seg{text: renderSegs(" ", segs...)}
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

// diffSeg renders a pull request's diff the way diffText does, with the
// additions and deletions coloured green and red — the same pair
// statusStyle already uses for a passing and a failing check — merged into
// one segment so it still costs one slot in a row's width budget. The file
// count carries no colour of its own, since it counts neither for nor
// against the change, and withFiles false leaves it off for a line with no
// room for it. The zero value's empty text marks a pull request with nothing
// to report.
func (s Styles) diffSeg(pr model.PullRequest, withFiles bool) seg {
	if pr.ChangedFiles == 0 && pr.Additions == 0 && pr.Deletions == 0 {
		return seg{}
	}
	segs := []seg{
		{text: fmt.Sprintf("+%d", pr.Additions), style: s.Success},
		{text: fmt.Sprintf("−%d", pr.Deletions), style: s.Failure},
	}
	if withFiles {
		files := "files"
		if pr.ChangedFiles == 1 {
			files = "file"
		}
		segs = append(segs, seg{text: fmt.Sprintf("· %d %s", pr.ChangedFiles, files), style: s.Meta})
	}
	return seg{text: renderSegs(" ", segs...)}
}

// justifyFirstFit is justify with a choice of right-hand texts, longest
// first: it uses the first that fits beside left without cutting it short,
// or none. A row's left-hand text is what identifies it, so the detail on
// the right gives way first.
func justifyFirstFit(width int, left string, rights ...string) string {
	for _, right := range rights {
		if right != "" && lenOf(left)+1+lenOf(right) <= width {
			return justify(width, left, right)
		}
	}
	return justify(width, left, "")
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
