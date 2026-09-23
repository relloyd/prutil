package model

import (
	"net/url"
	"strings"
	"unicode"
)

// SafeLine makes one value fit to be interpolated into a prompt: it drops the
// characters that carry meaning nobody can see and folds what is left onto a
// single line.
//
// Everything prutil puts into a prompt beyond its own wording comes from
// GitHub, and several of those values are written by people other than the
// reader. A pull request title is the author's. A check's name comes from a
// workflow file in the pull request's own head, and a legacy status context's
// description is set by anything holding commit-status write access.
//
// Two things are being prevented. The first is terminal injection: prutil does
// not know, and should not need to know, how herdr delivers a prompt into an
// agent's terminal, so an ESC sequence or a stray carriage return in the text
// might end a bracketed paste or submit a line early. Claude Code runs a
// prompt line beginning with "!" as a shell command without involving the
// model at all, which makes a newline in a field that is meant to be one line
// enough on its own. The second is the hidden-text problem hiddenRunes
// describes, here in a value prutil interpolates itself rather than one an
// agent goes and reads.
//
// Dropping rather than escaping is deliberate. An escaped control character is
// still a thing the reader has to reason about, and none of these values is
// worse off without them.
func SafeLine(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t' || unicode.IsSpace(r):
			// Any run of whitespace, newlines included, becomes one space, so
			// a single-line field stays one line.
			space = b.Len() > 0
		case r == 0x7F, r < 0x20, r >= 0x80 && r <= 0x9F:
			// C0 and C1 controls, ESC and DEL among them.
		case unicode.Is(unicode.Cf, r), r >= 0xE0000 && r <= 0xE007F:
			// Format characters and the tag block: zero-width, bidi controls
			// and the rest of what carries text invisibly.
		default:
			if space {
				b.WriteRune(' ')
				space = false
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ClipRunes shortens s to at most n runes, counted in runes rather than bytes
// so that a cap cannot cut a character in half.
func ClipRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return strings.TrimRight(string(runes[:n]), " ") + "…"
}

// SameHostURL returns raw when it is an absolute URL on the same host as want,
// and the empty string otherwise.
//
// A check's URL is a link prutil hands an agent to go and read. It arrives
// from the same API as everything else, and for a legacy status context
// anything with commit-status write access chooses it, so it is a place to
// point an agent somewhere of the attacker's choosing. The pull request's own
// URL is the host prutil is talking to, whether that is github.com or an
// enterprise install, so comparing against it needs no configuration.
func SameHostURL(raw, want string) string {
	if raw == "" || want == "" {
		return ""
	}
	got, err := url.Parse(raw)
	if err != nil || got.Host == "" {
		return ""
	}
	if got.Scheme != "https" && got.Scheme != "http" {
		return ""
	}
	wanted, err := url.Parse(want)
	if err != nil || wanted.Host == "" {
		return ""
	}
	if !strings.EqualFold(got.Host, wanted.Host) {
		return ""
	}
	return raw
}
