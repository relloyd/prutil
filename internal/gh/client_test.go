package gh_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/relloyd/prutil/internal/gh"
	"github.com/relloyd/prutil/internal/model"
)

// fakeRunner replays canned responses and records the arguments it was given.
type fakeRunner struct {
	responses [][]byte
	err       error
	calls     [][]string
}

func (f *fakeRunner) Run(_ context.Context, args ...string) ([]byte, error) {
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
