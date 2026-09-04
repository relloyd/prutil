package gh_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/relloyd/prutil/internal/gh"
	"github.com/relloyd/prutil/internal/model"
)

// fakeRunner replays canned responses and records the arguments it was given.
type fakeRunner struct {
	// mu guards everything below: the per-repo fill runs its batches
	// concurrently, so Run is called from several goroutines at once.
	mu        sync.Mutex
	responses [][]byte
	err       error
	calls     [][]string
}

func (f *fakeRunner) Run(_ context.Context, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, args)
	if f.err != nil {
		return nil, f.err
	}
	if len(f.responses) == 0 {
		return nil, errors.New("fakeRunner: no response left")
	}
	out := f.responses[0]
	f.responses = f.responses[1:]
	return out, nil
}

// callCount is the number of gh invocations so far.
func (f *fakeRunner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// argsOf returns the arguments of one recorded call, joined for easy matching.
func (f *fakeRunner) argsOf(i int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.calls[i], " ")
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return data
}

func TestListPullRequestsDecodesSearchResults(t *testing.T) {
	runner := &fakeRunner{responses: [][]byte{fixture(t, "search.json")}}
	client := gh.New(runner, 2)

	prs, err := client.ListPullRequests(context.Background(), "", 100)
	require.NoError(t, err)
	require.Len(t, prs, 3, "the Issue node must be dropped")

	// Newest first.
	assert.Equal(t, []string{"relloyd/other", "relloyd/prutil", "relloyd/third"},
		[]string{prs[0].Repo, prs[1].Repo, prs[2].Repo})

	first := prs[1]
	assert.Equal(t, 42, first.Number)
	assert.Equal(t, "Add retry to the uploader", first.Title, "titles are trimmed")
	assert.Equal(t, "https://github.com/relloyd/prutil/pull/42", first.URL)
	assert.Equal(t, "feat/uploader-retry", first.HeadRef)
	assert.Equal(t, "main", first.BaseRef)
	assert.Equal(t, model.MergeClean, first.Mergeable)
	assert.Equal(t, model.ReviewApproved, first.ReviewDecision)
	assert.Equal(t, model.StatusSuccess, first.Rollup)
	assert.Equal(t, 120, first.Additions)
	assert.Equal(t, 30, first.Deletions)
	assert.Equal(t, 7, first.ChangedFiles)
	assert.Equal(t, 4, first.Comments)
	assert.False(t, first.IsDraft)
	assert.Equal(t, time.Date(2026, 8, 30, 9, 0, 0, 0, time.UTC), first.CreatedAt.UTC())

	draft := prs[0]
	assert.True(t, draft.IsDraft)
	assert.Equal(t, model.MergeConflicting, draft.Mergeable)
	assert.Equal(t, model.ReviewChangesRequested, draft.ReviewDecision)
	assert.Equal(t, model.StatusFailure, draft.Rollup)

	noChecks := prs[2]
	assert.Equal(t, model.StatusUnknown, noChecks.Rollup, "a missing rollup is unknown, not failing")
	assert.Equal(t, model.ReviewNone, noChecks.ReviewDecision)
}

func TestListPullRequestsSendsExpectedArguments(t *testing.T) {
	runner := &fakeRunner{responses: [][]byte{fixture(t, "empty.json")}}
	client := gh.New(runner, 1)

	_, err := client.ListPullRequests(context.Background(), "", 10)
	require.NoError(t, err)
	require.Len(t, runner.calls, 1)

	args := runner.calls[0]
	assert.Equal(t, []string{"api", "graphql"}, args[:2])
	assert.Contains(t, args, "-f")
	assert.Contains(t, args, "-F")
	assert.Contains(t, args, "first=10", "the page size is capped by the limit")
	assert.Contains(t, args, "q="+gh.DefaultSearchQuery, "an empty query falls back to the default")
	assert.NotContains(t, strings.Join(args, " "), "after=", "the first page has no cursor")
}

func TestListPullRequestsPaginates(t *testing.T) {
	page1 := strings.NewReplacer(
		`"hasNextPage": false`, `"hasNextPage": true`,
	).Replace(string(fixture(t, "search.json")))
	runner := &fakeRunner{responses: [][]byte{[]byte(page1), fixture(t, "empty.json")}}
	client := gh.New(runner, 1)

	prs, err := client.ListPullRequests(context.Background(), "is:pr", 100)
	require.NoError(t, err)
	assert.Len(t, prs, 3)
	require.Len(t, runner.calls, 2, "a second page is requested when one is offered")
	assert.Contains(t, runner.calls[1], "after=Y3Vyc29yOjM=")
}

func TestListPullRequestsStopsAtTheLimit(t *testing.T) {
	runner := &fakeRunner{responses: [][]byte{fixture(t, "search.json")}}
	client := gh.New(runner, 1)

	prs, err := client.ListPullRequests(context.Background(), "", 3)
	require.NoError(t, err)
	assert.Len(t, prs, 3)
	assert.Len(t, runner.calls, 1)
}

func TestChecksDecodesBothContextTypes(t *testing.T) {
	runner := &fakeRunner{responses: [][]byte{fixture(t, "checks.json")}}
	client := gh.New(runner, 1)

	checks, err := client.Checks(context.Background(), model.Key{Repo: "relloyd/prutil", Number: 42})
	require.NoError(t, err)
	require.Len(t, checks, 4)

	assert.Equal(t, "test", checks[0].Name)
	assert.Equal(t, "CI", checks[0].Workflow)
	assert.Equal(t, model.StatusSuccess, checks[0].Status)
	assert.Equal(t, 80*time.Second, checks[0].Duration(time.Now()))

	assert.Equal(t, model.StatusFailure, checks[1].Status)

	assert.Equal(t, model.StatusPending, checks[2].Status, "an in-progress run is pending whatever its conclusion")
	assert.Empty(t, checks[2].Workflow, "a check run without a workflow run has no workflow name")

	assert.Equal(t, "codecov/project", checks[3].Name, "a legacy status context uses its context as the name")
	assert.Equal(t, "https://codecov.io/gh/relloyd/prutil/pull/42", checks[3].URL)
	assert.Equal(t, "85% of diff covered", checks[3].Description)
	assert.Equal(t, model.StatusSuccess, checks[3].Status)

	args := runner.calls[0]
	assert.Contains(t, args, "owner=relloyd")
	assert.Contains(t, args, "name=prutil")
	assert.Contains(t, args, "number=42")
}

func TestChecksRejectsMalformedRepository(t *testing.T) {
	client := gh.New(&fakeRunner{}, 1)

	_, err := client.Checks(context.Background(), model.Key{Repo: "prutil", Number: 1})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "malformed repository")
}

func TestGraphQLErrorsAreSurfaced(t *testing.T) {
	body := []byte(`{"data":null,"errors":[{"message":"Could not resolve to a User"}]}`)
	client := gh.New(&fakeRunner{responses: [][]byte{body}}, 1)

	_, err := client.ListPullRequests(context.Background(), "", 10)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Could not resolve to a User")
}

func TestRunnerErrorsAreSurfaced(t *testing.T) {
	client := gh.New(&fakeRunner{err: errors.New("exit status 4")}, 1)

	_, err := client.ListPullRequests(context.Background(), "", 10)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exit status 4")
}

func TestPingReportsUnauthenticated(t *testing.T) {
	client := gh.New(&fakeRunner{err: errors.New("You are not logged into any GitHub hosts")}, 1)

	err := client.Ping(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gh auth login")
}

func TestPingSucceedsWhenAuthenticated(t *testing.T) {
	runner := &fakeRunner{responses: [][]byte{[]byte("")}}
	client := gh.New(runner, 1)

	require.NoError(t, client.Ping(context.Background()))
	assert.Equal(t, []string{"auth", "status"}, runner.calls[0])
}

func TestCommandErrorPrefersStderrAndHidesTheQuery(t *testing.T) {
	err := &gh.CommandError{
		Args:   []string{"api", "graphql", "-f", "query=query($q: String!) { search }"},
		Stderr: "HTTP 401: Bad credentials",
		Err:    errors.New("exit status 1"),
	}

	assert.Contains(t, err.Error(), "HTTP 401: Bad credentials")
	assert.Contains(t, err.Error(), "query=...")
	assert.NotContains(t, err.Error(), "String!")
	assert.ErrorIs(t, err, err.Err)
}

func TestClosedSweepThatReachesTheEndCostsOneRequest(t *testing.T) {
	runner := &fakeRunner{responses: [][]byte{fixture(t, "closed_search.json")}}
	client := gh.New(runner, 4)

	prs, err := client.ListClosedPullRequests(context.Background(), gh.ClosedOptions{PerRepo: 3})
	require.NoError(t, err)

	// This is the property the whole design rests on: when the sweep reaches
	// the end of the search it already holds every closed pull request, so
	// repository discovery and the per-repo fill are skipped entirely.
	assert.Equal(t, 1, runner.callCount(), "an exhausted sweep issues no further requests")

	require.Len(t, prs, 4, "the fourth pull request from one repo is dropped, the Issue node with it")
	assert.Equal(t, []int{40, 39, 38, 5}, []int{prs[0].Number, prs[1].Number, prs[2].Number, prs[3].Number})
	assert.Equal(t, "relloyd/other", prs[3].Repo)

	assert.Equal(t, model.PRStateMerged, prs[0].State)
	assert.Equal(t, model.PRStateClosed, prs[1].State, "a pull request closed without merging stays distinct")
	assert.Equal(t, time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC), prs[0].ClosedAt.UTC())
	assert.Equal(t, time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC), prs[0].MergedAt.UTC())
	assert.True(t, prs[1].MergedAt.IsZero(), "an unmerged pull request has no merge time")
}

func TestClosedSweepSendsExpectedArguments(t *testing.T) {
	runner := &fakeRunner{responses: [][]byte{fixture(t, "closed_search.json")}}
	client := gh.New(runner, 1)

	_, err := client.ListClosedPullRequests(context.Background(), gh.ClosedOptions{})
	require.NoError(t, err)
	require.Equal(t, 1, runner.callCount())

	args := runner.argsOf(0)
	assert.Contains(t, args, "api graphql")
	assert.Contains(t, args, "q="+gh.DefaultClosedSearchQuery, "an empty query falls back to the default")
	assert.Contains(t, args, "closedAt", "the sweep selects the close timestamp")
	assert.Contains(t, args, "mergedAt")
	assert.NotContains(t, args, "mergeable", "mergeable means nothing once a pull request is closed")
}

func TestClosedFillQueriesOnlyTheRepositoriesThatCameUpShort(t *testing.T) {
	runner := &fakeRunner{responses: [][]byte{
		fixture(t, "closed_search_partial.json"),
		fixture(t, "discovery.json"),
		fixture(t, "repo_batch.json"),
	}}
	client := gh.New(runner, 4)

	// SweepLimit stops the sweep after one page, which is the case the
	// per-repo fill exists for: the window ran out before the search did.
	prs, err := client.ListClosedPullRequests(context.Background(), gh.ClosedOptions{PerRepo: 3, SweepLimit: 5})
	require.NoError(t, err)
	require.Equal(t, 3, runner.callCount(), "sweep, then discovery, then one batch")

	batch := runner.argsOf(2)
	assert.NotContains(t, batch, "repo:relloyd/prutil",
		"a repository the sweep already filled to PerRepo is not asked again")
	assert.Contains(t, batch, "repo:relloyd/quiet")
	assert.Contains(t, batch, "repo:acme/platform")
	assert.NotContains(t, batch, "relloyd/retired",
		"the query excludes archived repositories, so discovery drops them")
	assert.NotContains(t, batch, "not-a-valid-repo-name", "a malformed repository name is dropped")
	assert.Contains(t, batch, "$q0: String!", "each repository search is a variable, never spliced into the document")

	repos := make([]string, 0, len(prs))
	for _, pr := range prs {
		repos = append(repos, pr.Key().String())
	}
	assert.Contains(t, repos, "relloyd/quiet#12", "the fill adds what the sweep could not reach")
	assert.Contains(t, repos, "acme/platform#300")
	assert.Contains(t, repos, "relloyd/prutil#40", "the sweep results survive the merge")
}

func TestClosedResultsAreDedupedAndCapped(t *testing.T) {
	// The batch replies with a pull request the sweep already returned, which
	// is what happens whenever a repository is partly covered by both stages.
	overlap := `{"data": {"r0": {"nodes": [
		{"__typename": "PullRequest", "number": 40, "state": "MERGED",
		 "closedAt": "2026-09-02T10:00:00Z", "mergedAt": "2026-09-02T10:00:00Z",
		 "repository": {"nameWithOwner": "relloyd/prutil"}}
	]}}}`
	runner := &fakeRunner{responses: [][]byte{
		fixture(t, "closed_search_partial.json"),
		fixture(t, "discovery.json"),
		[]byte(overlap),
	}}
	client := gh.New(runner, 4)

	prs, err := client.ListClosedPullRequests(context.Background(), gh.ClosedOptions{PerRepo: 2, SweepLimit: 5})
	require.NoError(t, err)

	seen := map[model.Key]int{}
	perRepo := map[string]int{}
	for _, pr := range prs {
		seen[pr.Key()]++
		perRepo[pr.Repo]++
	}
	for key, n := range seen {
		assert.Equal(t, 1, n, "%s appears once", key)
	}
	for repo, n := range perRepo {
		assert.LessOrEqual(t, n, 2, "%s respects PerRepo", repo)
	}
}

func TestClosedSweepStopsAtTheLimit(t *testing.T) {
	runner := &fakeRunner{responses: [][]byte{fixture(t, "closed_search.json")}}
	client := gh.New(runner, 1)

	_, err := client.ListClosedPullRequests(context.Background(), gh.ClosedOptions{SweepLimit: 5, PerRepo: 3})
	require.NoError(t, err)
	assert.Contains(t, runner.argsOf(0), "first=5", "the page size is capped by the sweep limit")
}

func TestClosedFillPropagatesAFailedBatch(t *testing.T) {
	runner := &fakeRunner{responses: [][]byte{
		fixture(t, "closed_search_partial.json"),
		fixture(t, "discovery.json"),
		[]byte(`{"errors": [{"message": "API rate limit exceeded"}]}`),
	}}
	client := gh.New(runner, 4)

	_, err := client.ListClosedPullRequests(context.Background(), gh.ClosedOptions{PerRepo: 3, SweepLimit: 5})
	require.Error(t, err, "a failed fill surfaces rather than silently returning a partial list")
	assert.Contains(t, err.Error(), "rate limit")
}
