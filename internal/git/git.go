// Package git reads the small amount of local repository state prutil needs to
// decide which coding agent is sitting in the right place for a pull request.
//
// An agent is only the right target when its working directory is a checkout of
// the pull request's repository, ideally on the pull request's head branch.
// Neither GitHub nor herdr can answer that; only the checkout itself can.
package git

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/relloyd/prutil/internal/run"
)

// Binary is the CLI prutil shells out to, and Home the address named when it
// is missing.
const (
	Binary = "git"
	Home   = "https://git-scm.com"
)

// cacheLifetime is how long an identified checkout is trusted. A branch can be
// switched under prutil at any moment, so the answer is re-read often enough
// to notice; a handoff sent to the wrong branch is worse than a few extra
// process invocations.
const cacheLifetime = 15 * time.Second

// Checkout is what prutil learned about one directory.
type Checkout struct {
	// Root is the top level of the working tree, which for a linked worktree
	// is the worktree's own directory rather than the main checkout.
	Root string
	// Repo is owner/name, read from the origin remote. It is empty when the
	// checkout has no origin, or one prutil could not parse.
	Repo string
	// Branch is what is checked out there, empty when the head is detached.
	Branch string
	// Head is the commit checked out there, empty when git could not say.
	Head string
	// Upstream is the ref the branch pulls from on its remote, such as
	// refs/heads/feat/uploader-retry, or refs/pull/42/head for a branch made
	// from a pull request's own ref. It is empty for a detached head and for a
	// branch that tracks nothing.
	Upstream string
}

// Client reads repository state, caching what it learns for a short while.
type Client struct {
	run run.Runner

	mu    sync.Mutex
	cache map[string]entry
	now   func() time.Time
}

type entry struct {
	checkout Checkout
	at       time.Time
}

// New builds a client over any runner, which is what the tests use.
func New(r run.Runner) *Client {
	return &Client{run: r, cache: map[string]entry{}, now: time.Now}
}

// NewExec locates the git binary on PATH.
func NewExec() (*Client, error) {
	cmd, err := run.Look(Binary, Home)
	if err != nil {
		return nil, err
	}
	return New(cmd), nil
}

// Identify reports the repository, branch and commit checked out in dir. A
// directory that is not a working tree yields a zero Checkout rather than an error,
// because "this agent is not in a repository" is an ordinary answer.
func (c *Client) Identify(ctx context.Context, dir string) Checkout {
	if dir == "" {
		return Checkout{}
	}
	if got, ok := c.cached(dir); ok {
		return got
	}

	checkout := c.identify(ctx, dir)
	c.mu.Lock()
	c.cache[dir] = entry{checkout: checkout, at: c.now()}
	c.mu.Unlock()
	return checkout
}

// cached returns a fresh cache entry, if there is one. It drops what has gone
// stale while it is in there, so that a discovery walk over a directory of
// hundreds of checkouts does not leave an entry apiece behind it for the rest
// of the session.
func (c *Client) cached(dir string) (Checkout, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	got, ok := c.cache[dir]
	if ok && now.Sub(got.at) < cacheLifetime {
		return got.checkout, true
	}
	for key, entry := range c.cache {
		if now.Sub(entry.at) >= cacheLifetime {
			delete(c.cache, key)
		}
	}
	return Checkout{}, false
}

// identify does the reads without the cache in the way. A directory git will
// not treat as a working tree is not an error: an agent sitting in one is
// simply not a candidate for any pull request.
func (c *Client) identify(ctx context.Context, dir string) Checkout {
	root, ok := c.read(ctx, dir, "rev-parse", "--show-toplevel")
	if !ok {
		return Checkout{}
	}

	// A detached head reports an empty branch and exit status zero, and a
	// checkout with no origin, no commits yet or a branch that tracks nothing
	// is ordinary enough to pass over in silence.
	branch, _ := c.read(ctx, dir, "branch", "--show-current")
	remote, _ := c.read(ctx, dir, "remote", "get-url", "origin")
	head, _ := c.read(ctx, dir, "rev-parse", "HEAD")
	upstream := ""
	if branch != "" {
		upstream, _ = c.read(ctx, dir, "config", "--get", "branch."+branch+".merge")
	}

	return Checkout{Root: root, Repo: ParseRemote(remote), Branch: branch, Head: head, Upstream: upstream}
}

// Contains reports whether the commit checked out in dir has commit in its
// history: it is that commit, or has commits of its own on top. It is how
// prutil tells a checkout holding a pull request's work from one that merely
// has a similar branch name. A commit the checkout has never fetched is not
// contained, which is the honest answer. The result is not cached, because the
// question changes with the pull request.
func (c *Client) Contains(ctx context.Context, dir, commit string) bool {
	if dir == "" || !isCommitID(commit) {
		return false
	}
	_, ok := c.read(ctx, dir, "merge-base", "--is-ancestor", commit, "HEAD")
	return ok
}

// Clean reports whether dir is a working tree with nothing uncommitted in it:
// no staged or unstaged changes and no untracked files. A directory git will
// not answer for is not clean, because an agent handed one could be building
// on work the reader has not finished. The result is not cached; a reader's
// edits do not wait for a cache to expire.
func (c *Client) Clean(ctx context.Context, dir string) bool {
	if dir == "" {
		return false
	}
	out, ok := c.read(ctx, dir, "status", "--porcelain")
	return ok && out == ""
}

// isCommitID reports whether s is a commit hash, which is all Contains will
// put on git's command line in a position an option could otherwise take.
func isCommitID(s string) bool {
	if len(s) < 4 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// read runs one git command inside dir and returns its trimmed output,
// reporting false when git declined to answer.
func (c *Client) read(ctx context.Context, dir string, args ...string) (string, bool) {
	out, err := c.run.Run(ctx, append([]string{"-C", dir}, args...)...)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// ParseRemote reduces a remote URL to owner/name, returning the empty string
// when it is not a shape prutil recognises. It handles the three forms git
// hands out: https, ssh, and the scp-like host:path.
func ParseRemote(url string) string {
	url = strings.TrimSpace(url)
	if url == "" {
		return ""
	}
	url = strings.TrimSuffix(url, ".git")

	// The scp-like form has no scheme and separates host from path with a
	// colon, so the path is everything after it.
	if !strings.Contains(url, "://") {
		if _, path, ok := strings.Cut(url, ":"); ok {
			url = path
		}
	}

	parts := strings.Split(strings.Trim(url, "/"), "/")
	if len(parts) < 2 {
		return ""
	}
	owner, name := parts[len(parts)-2], parts[len(parts)-1]
	if owner == "" || name == "" || strings.Contains(owner, "@") {
		return ""
	}
	return owner + "/" + name
}
