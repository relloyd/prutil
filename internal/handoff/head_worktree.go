package handoff

import (
	"context"
	"strings"

	"github.com/relloyd/prutil/internal/herdr"
)

// headWorktree accepts a reader's existing worktree only when git confirms it
// is on the pull request's exact head commit, not just a similarly named branch.
func (d *Dispatcher) headWorktree(ctx context.Context, worktrees []herdr.Worktree, req Request) (herdr.Worktree, bool) {
	head := req.headOID()
	if head == "" || req.PR.HeadRef == "" {
		return herdr.Worktree{}, false
	}
	for _, worktree := range worktrees {
		if worktree.Branch != req.PR.HeadRef || strings.TrimSpace(worktree.Path) == "" {
			continue
		}
		checkout := d.git.Identify(ctx, worktree.Path)
		if strings.EqualFold(checkout.Repo, req.PR.Repo) &&
			checkout.Branch == req.PR.HeadRef && checkout.Head == head {
			return worktree, true
		}
	}
	return herdr.Worktree{}, false
}
