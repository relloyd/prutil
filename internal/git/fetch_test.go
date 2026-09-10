package git_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/relloyd/prutil/internal/git"
)

func TestFetchPullRequestRunsGitFetchAgainstOrigin(t *testing.T) {
	runner := &fakeRunner{replies: map[string]string{
		"-C /repo fetch origin pull/42/head:pr-42": "",
	}}

	err := git.New(runner).FetchPullRequest(context.Background(), "/repo", 42, "pr-42")
	require.NoError(t, err)
	assert.Equal(t, 1, runner.calls)
}

func TestFetchPullRequestValidatesTheRootBranchAndNumber(t *testing.T) {
	client := git.New(&fakeRunner{replies: map[string]string{}})

	for _, tc := range []struct {
		name   string
		root   string
		number int
		branch string
	}{
		{name: "empty root", root: "", number: 1, branch: "ok"},
		{name: "invalid number", root: "/repo", number: 0, branch: "ok"},
		{name: "empty branch", root: "/repo", number: 1, branch: ""},
		{name: "branch with space", root: "/repo", number: 1, branch: "bad branch"},
		{name: "branch with ref syntax", root: "/repo", number: 1, branch: "bad@{name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := client.FetchPullRequest(context.Background(), tc.root, tc.number, tc.branch)
			require.Error(t, err)
		})
	}
}

func TestFetchPullRequestSurfacesGitErrors(t *testing.T) {
	runner := &fakeRunner{
		replies: map[string]string{},
		fails:   map[string]bool{"-C /repo fetch origin pull/42/head:pr-42": true},
	}
	err := git.New(runner).FetchPullRequest(context.Background(), "/repo", 42, "pr-42")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not fetch pull request 42")
}
