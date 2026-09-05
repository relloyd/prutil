package gh

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

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
	// ListClosedPullRequests returns the user's most recently closed pull
	// requests, contributing at most PerRepo from any one repository.
	ListClosedPullRequests(ctx context.Context, opts ClosedOptions) (ClosedResult, error)
	// Checks returns the individual checks on a pull request's head commit.
	Checks(ctx context.Context, key model.Key) ([]model.Check, error)
}

// ClosedResult is the outcome of a recently-closed query.
type ClosedResult struct {
	// PRs is the grouped list, most recently closed first.
	PRs []model.PullRequest
	// Unavailable counts repositories the per-repo fill could not reach, whose
	// rows may therefore be missing from PRs. It is non-zero when GitHub
	// refused some of the batched searches, which a large organisation can
	// provoke; the sweep results are kept and shown regardless.
	Unavailable int
}

// ClosedOptions configures a recently-closed pull request query.
type ClosedOptions struct {
	// Query is the search the global sweep runs. Empty means
	// DefaultClosedSearchQuery.
	Query string
	// PerRepo caps how many pull requests any one repository contributes.
	PerRepo int
	// SweepLimit caps how far back the global sweep reaches before the per-repo
	// fill takes over.
	SweepLimit int
	// RepoLimit caps how many repositories the per-repo fill asks about.
	RepoLimit int
}

// withDefaults fills in the zero values so that callers can pass an empty
// struct and still get a sensible query.
func (o ClosedOptions) withDefaults() ClosedOptions {
	if strings.TrimSpace(o.Query) == "" {
		o.Query = DefaultClosedSearchQuery
	}
	if o.PerRepo < 1 {
		o.PerRepo = DefaultPerRepo
	}
	if o.SweepLimit < 1 {
		o.SweepLimit = DefaultSweepLimit
	}
	if o.RepoLimit < 1 {
		o.RepoLimit = DefaultRepoLimit
	}
	return o
}

// pageSize is the number of search results requested per round trip. GitHub
// caps search connections at 100.
const pageSize = 50

// Defaults for the recently-closed view.
const (
	// DefaultPerRepo is how many pull requests one repository contributes.
	DefaultPerRepo = 3
	// DefaultSweepLimit is how far back the global sweep reaches. Two pages
	// take a second or two and cover every closed pull request outright for
	// most accounts, which skips the per-repo fill entirely.
	DefaultSweepLimit = 100
	// DefaultRepoLimit caps the per-repo fan-out, and with it the worst case
	// cost of the view.
	DefaultRepoLimit = 30
)

const (
	// closedPageSize is the page size for the closed sweep. It is half what the
	// open list uses because a closed search selects more per node and runs
	// against far more results, and a full page of fifty measured close enough
	// to GitHub's document budget to fail intermittently.
	closedPageSize = 25
	// repoBatchSize is how many repositories share one GraphQL document.
	// GitHub gives a document roughly ten seconds before the edge returns 502,
	// and latency grows with the number of aliased searches: eight measured
	// about four seconds against public repositories, which sounds safe until a
	// large organisation's permission checks spend the remaining margin. Three
	// measured about two seconds. Do not raise this to save round trips; the
	// rate limit charge is one point either way.
	repoBatchSize = 3
	// batchTimeout bounds one batched document. The budget starts once the
	// request has a semaphore slot, so a batch waiting its turn is not charged
	// for the wait.
	batchTimeout = 20 * time.Second
	// discoverPageSize is the page size for repository discovery. It selects
	// one small field per node, so a full page stays cheap where the sweep's
	// heavier selection does not.
	discoverPageSize = 100
	// discoverPages caps how far past the sweep discovery reads. Three pages
	// name every repository in the six hundred most recently closed pull
	// requests, which is far enough back for a view of recent work.
	discoverPages = 3
)

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

// ListClosedPullRequests implements Client. It works in two stages. First one
// global sweep, ordered by recency across every repository at once, which is
// enough on its own whenever it reaches the end of the search. When it does
// not, a couple of busy repositories may have crowded the rest out of the
// window, so the repositories that came up short are then asked directly, in
// batched documents.
func (c *CLI) ListClosedPullRequests(ctx context.Context, opts ClosedOptions) (ClosedResult, error) {
	opts = opts.withDefaults()

	swept, err := c.sweepClosed(ctx, opts.Query, opts.SweepLimit)
	if err != nil {
		// The sweep is the only stage that can leave nothing to show, so it is
		// the only one whose failure is fatal.
		return ClosedResult{}, err
	}
	// The sweep read the search to its end, so it already holds every closed
	// pull request there is and grouping it is exact. This is the common case,
	// and it makes the whole view cost a single request.
	if swept.exhausted {
		return ClosedResult{PRs: groupClosed(swept.prs, opts.PerRepo)}, nil
	}
	return c.fillPerRepo(ctx, opts, swept)
}

// sweep is the outcome of the global sweep.
type sweep struct {
	prs []model.PullRequest
	// exhausted reports that the search ran out before the window filled, so
	// the grouping is already exact and the fill can be skipped.
	exhausted bool
	// cursor is where the search left off, so repository discovery can read on
	// from the same ordering rather than starting again.
	cursor string
}

// sweepClosed runs the global search, following pages until it has limit
// results or the search runs out. It reports whether the search was exhausted,
// which is what tells the caller that the per-repo fill can be skipped.
func (c *CLI) sweepClosed(ctx context.Context, query string, limit int) (sweep, error) {
	var out sweep
	for len(out.prs) < limit {
		want := min(limit-len(out.prs), closedPageSize)
		vars := map[string]any{"q": query, "first": want}
		if out.cursor != "" {
			vars["after"] = out.cursor
		}

		var page listResponse
		if err := c.graphql(ctx, closedListQuery, vars, &page); err != nil {
			return sweep{}, err
		}
		for _, node := range page.Search.Nodes {
			if pr, ok := node.toPullRequest(); ok {
				out.prs = append(out.prs, pr)
			}
		}
		if !page.Search.PageInfo.HasNextPage || page.Search.PageInfo.EndCursor == "" {
			out.exhausted = true
			return out, nil
		}
		out.cursor = page.Search.PageInfo.EndCursor
	}
	return out, nil
}

// fillPerRepo tops up the repositories the global sweep did not see PerRepo
// pull requests for, by asking those repositories directly.
func (c *CLI) fillPerRepo(ctx context.Context, opts ClosedOptions, swept sweep) (ClosedResult, error) {
	grouped := groupClosed(swept.prs, opts.PerRepo)
	filled := make(map[string]int, len(grouped))
	for _, pr := range grouped {
		filled[pr.Repo]++
	}

	repos, err := c.discoverRepos(ctx, opts.Query, swept.cursor)
	if err != nil {
		// Discovery is a small, cheap request. If it fails, something is wrong
		// beyond a slow search, so say so rather than quietly showing a list
		// that is missing whole repositories.
		return ClosedResult{}, err
	}

	want := make([]string, 0, opts.RepoLimit)
	for _, repo := range repos {
		if filled[repo] >= opts.PerRepo {
			continue
		}
		want = append(want, repo)
		if len(want) >= opts.RepoLimit {
			break
		}
	}
	if len(want) == 0 {
		return ClosedResult{PRs: grouped}, nil
	}

	extra, unavailable := c.searchRepoBatches(ctx, opts, want)
	return ClosedResult{
		PRs:         groupClosed(dedupe(append(swept.prs, extra...)), opts.PerRepo),
		Unavailable: unavailable,
	}, nil
}

// searchRepoBatches asks about repos in batched documents. The semaphore inside
// graphql already caps how many gh processes run at once, so every batch can be
// started and left to that to throttle.
//
// A batch that fails is counted, not returned as an error. GitHub answers an
// over-budget document with a 502, and one unlucky batch out of ten should cost
// the rows of the repositories in it, not the whole view. The count is how many
// repositories went unanswered.
func (c *CLI) searchRepoBatches(ctx context.Context, opts ClosedOptions, repos []string) ([]model.PullRequest, int) {
	var (
		mu          sync.Mutex
		wg          sync.WaitGroup
		out         []model.PullRequest
		unavailable int
	)

	for start := 0; start < len(repos); start += repoBatchSize {
		doc, vars, covered := buildRepoBatchQuery(opts.Query, repos[start:min(start+repoBatchSize, len(repos))], opts.PerRepo)
		if len(covered) == 0 {
			continue
		}

		wg.Add(1)
		go func() {
			defer wg.Done()

			var resp repoBatchResponse
			err := c.graphqlWithin(ctx, batchTimeout, doc, vars, &resp)

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				unavailable += len(covered)
				return
			}
			for _, alias := range resp {
				for _, node := range alias.Nodes {
					if pr, ok := node.toPullRequest(); ok {
						out = append(out, pr)
					}
				}
			}
		}()
	}
	wg.Wait()

	return out, unavailable
}

// discoverRepos names the repositories worth a direct query, by reading on
// from where the sweep stopped and collecting repository names. Because the
// names come from the same search, every one of them certainly holds matching
// pull requests, so no alias in the fill is spent learning nothing.
//
// Names come back in the search's own recency order, so a caller trimming to a
// limit keeps the repositories whose work is most recent.
func (c *CLI) discoverRepos(ctx context.Context, query, cursor string) ([]string, error) {
	seen := map[string]bool{}
	var out []string

	for page := 0; page < discoverPages && cursor != ""; page++ {
		vars := map[string]any{"q": query, "first": discoverPageSize, "after": cursor}

		var resp repoNamesResponse
		if err := c.graphql(ctx, repoNamesQuery, vars, &resp); err != nil {
			return nil, err
		}
		for _, node := range resp.Search.Nodes {
			name := node.Repository.NameWithOwner
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, name)
		}
		if !resp.Search.PageInfo.HasNextPage {
			break
		}
		cursor = resp.Search.PageInfo.EndCursor
	}
	return out, nil
}

// groupClosed orders pull requests by close date and keeps at most perRepo from
// any one repository. It sorts prs in place.
func groupClosed(prs []model.PullRequest, perRepo int) []model.PullRequest {
	model.SortByClosedDesc(prs)
	return model.TopNPerRepo(prs, perRepo)
}

// dedupe drops repeats, keeping the first of each. The sweep and the per-repo
// fill overlap wherever a repository was partly covered by both.
func dedupe(prs []model.PullRequest) []model.PullRequest {
	seen := make(map[model.Key]bool, len(prs))
	out := prs[:0:0]
	for _, pr := range prs {
		if seen[pr.Key()] {
			continue
		}
		seen[pr.Key()] = true
		out = append(out, pr)
	}
	return out
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
	return c.graphqlWithin(ctx, 0, doc, vars, out)
}

// graphqlWithin is graphql with its own deadline. A budget of zero means the
// caller's context is the only limit.
func (c *CLI) graphqlWithin(ctx context.Context, budget time.Duration, doc string, vars map[string]any, out any) error {
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-ctx.Done():
		return ctx.Err()
	}

	// The budget starts here rather than on entry, so a request that waited for
	// a semaphore slot is not charged for the wait.
	if budget > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, budget)
		defer cancel()
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
