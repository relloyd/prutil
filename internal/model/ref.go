package model

import (
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// repoPart is one half of an owner/name repository identifier, in the
// characters GitHub allows in either.
var repoPart = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ErrNotAReference is what ParseReference returns for text it cannot read as a
// pull request.
var ErrNotAReference = errors.New("expected a pull request URL or owner/repo#number")

// ParseReference reads a pull request the way a reader is likely to paste one:
// its URL, with or without a trailing tab such as /files or a #fragment, or
// GitHub's own shorthand, owner/repo#12. owner/repo/pull/12 is accepted too,
// being the URL without its host.
//
// The host is not checked. prutil asks gh about the key, and gh answers for
// whichever host it is authenticated against, so an enterprise URL works
// exactly as a github.com one does, and a URL for somewhere gh cannot reach
// fails when it is looked up rather than here.
func ParseReference(text string) (Key, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Key{}, ErrNotAReference
	}

	if repo, number, ok := strings.Cut(text, "#"); ok && !strings.Contains(text, "://") {
		return keyOf(repo, number)
	}

	path := text
	if strings.Contains(text, "://") {
		u, err := url.Parse(text)
		if err != nil {
			return Key{}, ErrNotAReference
		}
		path = u.Path
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 4 || parts[2] != "pull" {
		return Key{}, ErrNotAReference
	}
	return keyOf(parts[0]+"/"+parts[1], parts[3])
}

// keyOf validates the two halves of a reference.
func keyOf(repo, number string) (Key, error) {
	owner, name, ok := strings.Cut(strings.TrimSpace(repo), "/")
	if !ok || !repoPart.MatchString(owner) || !repoPart.MatchString(name) {
		return Key{}, ErrNotAReference
	}
	n, err := strconv.Atoi(strings.TrimSpace(number))
	if err != nil || n < 1 {
		return Key{}, ErrNotAReference
	}
	return Key{Repo: owner + "/" + name, Number: n}, nil
}
