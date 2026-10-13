package gh_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/relloyd/prutil/internal/gh"
	"github.com/relloyd/prutil/internal/model"
)

func checksPage(t *testing.T, head, cursor string, hasNext bool, nodes ...any) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"pullRequest": map[string]any{
					"commits": map[string]any{
						"nodes": []any{map[string]any{
							"commit": map[string]any{
								"oid": head,
								"statusCheckRollup": map[string]any{
									"contexts": map[string]any{
										"pageInfo": map[string]any{"hasNextPage": hasNext, "endCursor": cursor},
										"nodes":    nodes,
									},
								},
							},
						}},
					},
				},
			},
		},
	})
	require.NoError(t, err)
	return body
}

func run(name, app, workflow string, id int, started, suiteCreated, status, conclusion string) map[string]any {
	var startedAt any
	if started != "" {
		startedAt = started
	}
	return map[string]any{
		"__typename": "CheckRun",
		"name":       name,
		"databaseId": id,
		"detailsUrl": fmt.Sprintf("https://github.com/a/b/actions/runs/%d", id),
		"startedAt":  startedAt,
		"status":     status,
		"conclusion": conclusion,
		"checkSuite": map[string]any{
			"createdAt":   suiteCreated,
			"app":         map[string]any{"id": app, "slug": app},
			"workflowRun": map[string]any{"workflow": map[string]any{"id": workflow, "name": workflow}},
		},
	}
}

func status(name, date, state string) map[string]any {
	return map[string]any{
		"__typename": "StatusContext",
		"context":    name,
		"state":      state,
		"createdAt":  date,
		"targetUrl":  "https://github.com/a/b/status/" + name,
	}
}

func TestChecksKeepsOnlyTheLatestAttemptDespiteShuffledHistory(t *testing.T) {
	old := "2026-09-30T10:00:00Z"
	middle := "2026-10-09T10:00:00Z"
	newest := "2026-10-12T10:00:00Z"
	runner := &fakeRunner{responses: [][]byte{checksPage(t, "head", "", false,
		run("Validate PR title", "actions", "validate", 12, newest, newest, "COMPLETED", "SUCCESS"),
		run("lint", "actions", "ci", 20, old, old, "COMPLETED", "FAILURE"),
		run("Validate PR title", "actions", "validate", 10, old, old, "COMPLETED", "FAILURE"),
		run("test", "actions", "ci", 31, newest, newest, "COMPLETED", "FAILURE"),
		run("lint", "actions", "ci", 21, "", old, "QUEUED", ""),
		run("Validate PR title", "actions", "validate", 11, middle, middle, "COMPLETED", "FAILURE"),
		run("test", "actions", "ci", 30, old, old, "COMPLETED", "SUCCESS"),
	)}}

	result, err := gh.New(runner, 1).Checks(context.Background(), model.Key{Repo: "a/b", Number: 1})
	require.NoError(t, err)
	assert.Equal(t, "head", result.HeadOID)
	require.Len(t, result.Checks, 3)
	assert.Equal(t, []string{"Validate PR title", "lint", "test"},
		[]string{result.Checks[0].Name, result.Checks[1].Name, result.Checks[2].Name},
		"checks keep their first-seen positions when a newer attempt replaces one")
	assert.Equal(t, model.StatusSuccess, result.Checks[0].Status, "old failures do not survive a successful rerun")
	assert.Equal(t, model.StatusPending, result.Checks[1].Status, "a queued rerun supersedes a failure even in the same old suite")
	assert.Contains(t, result.Checks[1].URL, "/21")
	assert.Equal(t, model.StatusFailure, result.Checks[2].Status, "a real latest failure remains visible")
}

func TestChecksReproducesTheSeventySevenRunsBehindSre555(t *testing.T) {
	old := "2026-09-30T13:56:44Z"
	failedAgain := "2026-10-09T15:29:17Z"
	latest := "2026-10-12T14:54:30Z"
	nodes := []any{
		run("Validate PR title", "actions", "lint-pr", 1, old, old, "COMPLETED", "FAILURE"),
		run("Validate PR title", "actions", "lint-pr", 2, failedAgain, failedAgain, "COMPLETED", "FAILURE"),
	}
	for i := range 48 {
		nodes = append(nodes, run(fmt.Sprintf("skipped-%02d", i), "actions", "ci", i+3, old, old, "COMPLETED", "SKIPPED"))
	}
	for i := range 21 {
		nodes = append(nodes, run(fmt.Sprintf("passed-%02d", i), "actions", "ci", i+51, old, old, "COMPLETED", "SUCCESS"))
	}
	for i := range 6 {
		at := fmt.Sprintf("2026-10-09T14:%02d:00Z", 40+i)
		if i == 5 {
			at = latest
		}
		nodes = append(nodes, run("Validate PR title", "actions", "lint-pr", i+72, at, at, "COMPLETED", "SUCCESS"))
	}
	require.Len(t, nodes, 77)
	runner := &fakeRunner{responses: [][]byte{
		checksPage(t, "73d9b5ce0a1a97a23ac24ac6ceeb912aa5d7de9d", "", false, nodes...),
	}}

	result, err := gh.New(runner, 1).Checks(context.Background(), model.Key{Repo: "dojo-engineering/sre", Number: 555})
	require.NoError(t, err)
	assert.Equal(t, model.CheckCounts{Success: 22, Other: 48, Total: 70},
		model.CountChecks(result.Checks), "old failures on the same head cannot override the newest passing run")
}

func TestChecksSeparatesAppsWorkflowsAndLegacyStatusContexts(t *testing.T) {
	old := "2026-10-09T10:00:00Z"
	newest := "2026-10-12T10:00:00Z"
	runner := &fakeRunner{responses: [][]byte{checksPage(t, "head", "", false,
		run("shared", "app-one", "workflow-one", 10, old, old, "COMPLETED", "FAILURE"),
		run("shared", "app-two", "workflow-one", 11, newest, newest, "COMPLETED", "FAILURE"),
		run("shared", "app-one", "workflow-two", 12, newest, newest, "COMPLETED", "SUCCESS"),
		status("shared", newest, "SUCCESS"),
		status("shared", old, "FAILURE"),
		status("other", old, "PENDING"),
		run("shared", "app-one", "workflow-one", 13, newest, newest, "COMPLETED", "SUCCESS"),
	)}}

	result, err := gh.New(runner, 1).Checks(context.Background(), model.Key{Repo: "a/b", Number: 1})
	require.NoError(t, err)
	require.Len(t, result.Checks, 5, "same-name checks remain separate across types, apps, and workflows")
	assert.Equal(t, []model.Status{
		model.StatusSuccess, model.StatusFailure, model.StatusSuccess, model.StatusSuccess, model.StatusPending,
	}, []model.Status{
		result.Checks[0].Status, result.Checks[1].Status, result.Checks[2].Status, result.Checks[3].Status, result.Checks[4].Status,
	})
	assert.Equal(t, "workflow-one", result.Checks[0].Workflow)
	assert.Equal(t, "workflow-two", result.Checks[2].Workflow)
	assert.Empty(t, result.Checks[3].Workflow)
	assert.Contains(t, result.Checks[0].URL, "/13")
}

func TestChecksPaginatesPastOneHundredContextsAndNormalizesAcrossPages(t *testing.T) {
	old := "2026-10-09T10:00:00Z"
	newest := "2026-10-12T10:00:00Z"
	first := []any{run("title", "actions", "ci", 1, old, old, "COMPLETED", "FAILURE")}
	for i := range 99 {
		first = append(first, run(fmt.Sprintf("check-%02d", i), "actions", "ci", i+2, old, old, "COMPLETED", "SUCCESS"))
	}
	runner := &fakeRunner{responses: [][]byte{
		checksPage(t, "head", "cursor-100", true, first...),
		checksPage(t, "head", "", false, run("title", "actions", "ci", 200, newest, newest, "COMPLETED", "SUCCESS"),
			status("legacy", newest, "PENDING")),
	}}

	result, err := gh.New(runner, 1).Checks(context.Background(), model.Key{Repo: "a/b", Number: 1})
	require.NoError(t, err)
	assert.Equal(t, "head", result.HeadOID)
	require.Len(t, result.Checks, 101)
	assert.Equal(t, model.StatusSuccess, result.Checks[0].Status)
	assert.Equal(t, "check-00", result.Checks[1].Name)
	assert.Equal(t, "legacy", result.Checks[100].Name)
	require.Equal(t, 2, runner.callCount())
	assert.Contains(t, runner.argsOf(0), "contexts(first: 100, after: $after)")
	assert.Contains(t, runner.argsOf(0), "pageInfo { hasNextPage endCursor }")
	assert.NotContains(t, runner.argsOf(0), "after=cursor-100")
	assert.Contains(t, runner.argsOf(1), "after=cursor-100")
}

func TestChecksReturnsNormalizedPartialResultsWhenSecondPageFails(t *testing.T) {
	old := "2026-10-09T10:00:00Z"
	newest := "2026-10-12T10:00:00Z"
	runner := &fakeRunner{responses: [][]byte{
		checksPage(t, "head", "cursor", true,
			run("title", "actions", "ci", 1, old, old, "COMPLETED", "FAILURE"),
			run("title", "actions", "ci", 2, newest, newest, "COMPLETED", "SUCCESS")),
		[]byte(`{"data":null,"errors":[{"message":"page two unavailable"}]}`),
	}}

	result, err := gh.New(runner, 1).Checks(context.Background(), model.Key{Repo: "a/b", Number: 1})
	require.ErrorContains(t, err, "page two unavailable")
	assert.Equal(t, "head", result.HeadOID)
	require.Len(t, result.Checks, 1)
	assert.Equal(t, model.StatusSuccess, result.Checks[0].Status)
}

func TestChecksRejectsAPageFromAnotherHeadWithoutMixingChecks(t *testing.T) {
	date := "2026-10-12T10:00:00Z"
	runner := &fakeRunner{responses: [][]byte{
		checksPage(t, "old-head", "cursor", true, run("title", "actions", "ci", 1, date, date, "COMPLETED", "FAILURE")),
		checksPage(t, "new-head", "", false, run("title", "actions", "ci", 2, date, date, "COMPLETED", "SUCCESS"),
			status("new-check", date, "SUCCESS")),
	}}

	result, err := gh.New(runner, 1).Checks(context.Background(), model.Key{Repo: "a/b", Number: 1})
	require.ErrorContains(t, err, "head changed")
	assert.Equal(t, "old-head", result.HeadOID)
	require.Len(t, result.Checks, 1)
	assert.Equal(t, model.StatusFailure, result.Checks[0].Status)
}

func TestChecksReportsMissingNextCursorWithoutLooping(t *testing.T) {
	date := "2026-10-12T10:00:00Z"
	runner := &fakeRunner{responses: [][]byte{
		checksPage(t, "head", "", true, status("legacy", date, "FAILURE")),
	}}

	result, err := gh.New(runner, 1).Checks(context.Background(), model.Key{Repo: "a/b", Number: 1})
	require.ErrorContains(t, err, "no new cursor")
	assert.Equal(t, "head", result.HeadOID)
	require.Len(t, result.Checks, 1)
	assert.Equal(t, 1, runner.callCount())
}

func TestChecksFirstPageErrorReturnsNoPartialResult(t *testing.T) {
	runner := &fakeRunner{responses: [][]byte{
		[]byte(`{"data":null,"errors":[{"message":"first page unavailable"}]}`),
	}}
	result, err := gh.New(runner, 1).Checks(context.Background(), model.Key{Repo: "a/b", Number: 1})
	require.ErrorContains(t, err, "first page unavailable")
	assert.Empty(t, result.HeadOID)
	assert.Empty(t, result.Checks)
}

func TestChecksUsesRunIDWhenTimestampsTie(t *testing.T) {
	date := "2026-10-12T10:00:00Z"
	runner := &fakeRunner{responses: [][]byte{checksPage(t, "head", "", false,
		run("title", "actions", "ci", 2, date, date, "COMPLETED", "SUCCESS"),
		run("title", "actions", "ci", 1, date, date, "COMPLETED", "FAILURE"),
	)}}
	result, err := gh.New(runner, 1).Checks(context.Background(), model.Key{Repo: "a/b", Number: 1})
	require.NoError(t, err)
	require.Len(t, result.Checks, 1)
	assert.Equal(t, model.StatusSuccess, result.Checks[0].Status)
	assert.True(t, strings.HasSuffix(result.Checks[0].URL, "/2"))
}

func TestChecksUsesSuiteCreationWhenAQueuedRunHasNoStartOrID(t *testing.T) {
	old := "2026-10-09T10:00:00Z"
	newest := "2026-10-12T10:00:00Z"
	runner := &fakeRunner{responses: [][]byte{checksPage(t, "head", "", false,
		run("title", "actions", "ci", 0, old, old, "COMPLETED", "FAILURE"),
		run("title", "actions", "ci", 0, "", newest, "QUEUED", ""),
	)}}
	result, err := gh.New(runner, 1).Checks(context.Background(), model.Key{Repo: "a/b", Number: 1})
	require.NoError(t, err)
	require.Len(t, result.Checks, 1)
	assert.Equal(t, model.StatusPending, result.Checks[0].Status)
}

// runOn is run for a workflow triggered by a particular event.
func runOn(event, name, app, workflow string, id int, started, status, conclusion string) map[string]any {
	node := run(name, app, workflow, id, started, started, status, conclusion)
	node["checkSuite"].(map[string]any)["workflowRun"].(map[string]any)["event"] = event
	return node
}

func TestChecksKeepsAWorkflowsRunsForDifferentEventsApart(t *testing.T) {
	old := "2026-10-09T10:00:00Z"
	newest := "2026-10-12T10:00:00Z"
	runner := &fakeRunner{responses: [][]byte{checksPage(t, "head", "", false,
		runOn("push", "test", "actions", "ci", 10, old, "COMPLETED", "FAILURE"),
		runOn("pull_request", "test", "actions", "ci", 11, newest, "COMPLETED", "SUCCESS"),
	)}}

	result, err := gh.New(runner, 1).Checks(context.Background(), model.Key{Repo: "a/b", Number: 1})
	require.NoError(t, err)
	assert.Equal(t, model.CheckCounts{Success: 1, Failure: 1, Total: 2}, model.CountChecks(result.Checks),
		"GitHub shows test (push) and test (pull_request) as two checks, and a pass in one does not answer a failure in the other")
}
