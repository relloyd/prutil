package git

import (
	"context"
	"fmt"
	"strings"
)

// PullRequestFetcher fetches pull request heads into local branches.
type PullRequestFetcher interface {
	FetchPullRequest(ctx context.Context, root string, number int, branch string) error
}

// FetchPullRequest runs:
//
//	git -C <root> fetch origin pull/<number>/head:<branch>
//
// root is expected to be a validated checkout root, such as Resolver.Resolve's
// Checkout.Root result.
func (c *Client) FetchPullRequest(ctx context.Context, root string, number int, branch string) error {
	root, err := resolvePath(root)
	if err != nil {
		return fmt.Errorf("repository root is invalid: %w", err)
	}
	if number < 1 {
		return fmt.Errorf("pull request number must be positive")
	}
	branch = strings.TrimSpace(branch)
	if err := validateBranch(branch); err != nil {
		return fmt.Errorf("branch name is invalid: %w", err)
	}

	spec := fmt.Sprintf("pull/%d/head:%s", number, branch)
	if _, err := c.run.Run(ctx, "-C", root, "fetch", "origin", spec); err != nil {
		return fmt.Errorf("could not fetch pull request %d into %s: %w", number, branch, err)
	}
	return nil
}

func validateBranch(branch string) error {
	if branch == "" {
		return fmt.Errorf("branch name is empty")
	}
	if strings.HasPrefix(branch, "-") {
		return fmt.Errorf("branch name must not start with '-'")
	}
	if strings.ContainsAny(branch, " \t\r\n") {
		return fmt.Errorf("branch name must not contain whitespace")
	}
	if strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") || strings.Contains(branch, "//") {
		return fmt.Errorf("branch name must not contain empty path components")
	}
	if strings.HasSuffix(branch, ".lock") {
		return fmt.Errorf("branch name must not end with .lock")
	}
	if strings.Contains(branch, "..") {
		return fmt.Errorf("branch name must not contain '..'")
	}
	if strings.Contains(branch, "@{") {
		return fmt.Errorf("branch name must not contain '@{'")
	}
	if strings.ContainsAny(branch, `~^:?*[\]`) {
		return fmt.Errorf("branch name contains a character git does not allow")
	}
	if strings.HasSuffix(branch, ".") {
		return fmt.Errorf("branch name must not end with '.'")
	}
	return nil
}
