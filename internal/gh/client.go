package gh

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/relloyd/prutil/internal/model"
)

// Client is the GitHub surface the UI depends on. Keeping it an interface lets
// the TUI tests run without a network or a gh binary.
type Client interface {
	// Ping fails when gh is not authenticated.
	Ping(ctx context.Context) error
	// ListPullRequests returns the headline information for every pull request
	// matching the search query, newest first.
	ListPullRequests(ctx context.Context, query string, limit int) ([]model.PullRequest, error)
	// Checks returns the individual checks on a pull request's head commit.
	Checks(ctx context.Context, key model.Key) ([]model.Check, error)
}

// pageSize is the number of search results requested per round trip. GitHub
// caps search connections at 100.
const pageSize = 50

// CLI is a Client backed by the gh command line tool.
type CLI struct {
	runner Runner
	sem    chan struct{}
}

// New returns a CLI that runs at most concurrency gh processes at a time, so
// that prefetching check detail for a long list cannot fork a process per PR.
func New(runner Runner, concurrency int) *CLI {
	if concurrency < 1 {
		concurrency = 1
	}
	return &CLI{runner: runner, sem: make(chan struct{}, concurrency)}
}

// Ping implements Client.
func (c *CLI) Ping(ctx context.Context) error {
	if _, err := c.runner.Run(ctx, "auth", "status"); err != nil {
		return fmt.Errorf("gh is not authenticated, run `gh auth login`: %w", err)
	}
	return nil
}

// ListPullRequests implements Client.
func (c *CLI) ListPullRequests(ctx context.Context, query string, limit int) ([]model.PullRequest, error) {
	if strings.TrimSpace(query) == "" {
		query = DefaultSearchQuery
	}
	if limit <= 0 {
		limit = pageSize
	}

	var (
		prs    []model.PullRequest
		cursor string
	)
	for len(prs) < limit {
		want := min(limit-len(prs), pageSize)
		vars := map[string]any{"q": query, "first": want}
		if cursor != "" {
			vars["after"] = cursor
		}

		var page listResponse
		if err := c.graphql(ctx, listQuery, vars, &page); err != nil {
			return nil, err
		}
		for _, node := range page.Search.Nodes {
			if pr, ok := node.toPullRequest(); ok {
				prs = append(prs, pr)
			}
		}
		if !page.Search.PageInfo.HasNextPage || page.Search.PageInfo.EndCursor == "" {
			break
		}
		cursor = page.Search.PageInfo.EndCursor
	}

	model.SortByCreatedDesc(prs)
	return prs, nil
}

// Checks implements Client.
func (c *CLI) Checks(ctx context.Context, key model.Key) ([]model.Check, error) {
	owner, name := key.Owner()
	if owner == "" || name == "" {
		return nil, fmt.Errorf("malformed repository %q", key.Repo)
	}

	var resp detailResponse
	vars := map[string]any{"owner": owner, "name": name, "number": key.Number}
	if err := c.graphql(ctx, detailQuery, vars, &resp); err != nil {
		return nil, err
	}

	rollup := resp.Repository.PullRequest.Commits.rollup()
	if rollup == nil {
		return nil, nil
	}
	checks := make([]model.Check, 0, len(rollup.Contexts.Nodes))
	for _, node := range rollup.Contexts.Nodes {
		checks = append(checks, node.toCheck())
	}
	return checks, nil
}

// graphql runs one gh api graphql call and decodes its data envelope into out.
func (c *CLI) graphql(ctx context.Context, doc string, vars map[string]any, out any) error {
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-ctx.Done():
		return ctx.Err()
	}

	stdout, err := c.runner.Run(ctx, graphqlArgs(doc, vars)...)
	if err != nil {
		// gh prints GraphQL errors on stderr and exits non-zero, so its own
		// message is the most useful thing we can show.
		return err
	}

	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(stdout, &envelope); err != nil {
		return fmt.Errorf("decoding gh response: %w", err)
	}
	if len(envelope.Errors) > 0 {
		messages := make([]string, 0, len(envelope.Errors))
		for _, e := range envelope.Errors {
			messages = append(messages, e.Message)
		}
		return fmt.Errorf("github: %s", strings.Join(messages, "; "))
	}
	if len(envelope.Data) == 0 {
		return fmt.Errorf("github returned no data")
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return fmt.Errorf("decoding gh response: %w", err)
	}
	return nil
}

// graphqlArgs builds the gh argument list. Variables are sorted so that the
// command is deterministic and can be asserted in tests.
func graphqlArgs(doc string, vars map[string]any) []string {
	args := []string{"api", "graphql", "-f", "query=" + doc}
	names := make([]string, 0, len(vars))
	for name := range vars {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		switch v := vars[name].(type) {
		case int:
			// -F asks gh to send the value as a JSON number.
			args = append(args, "-F", name+"="+strconv.Itoa(v))
		default:
			args = append(args, "-f", fmt.Sprintf("%s=%v", name, v))
		}
	}
	return args
}
