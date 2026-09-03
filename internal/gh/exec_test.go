package gh_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/relloyd/prutil/internal/gh"
	"github.com/relloyd/prutil/internal/model"
)

// stubGH puts a shell script named gh at the front of PATH for the duration of
// the test, so the real process path is exercised without a real GitHub.
func stubGH(t *testing.T, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stub gh is a shell script")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "gh")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestExecRunnerDrivesTheRealProcess(t *testing.T) {
	wd, err := os.Getwd()
	require.NoError(t, err)

	stubGH(t, `
case "$1" in
  auth) exit 0 ;;
  api)
    if printf '%s\n' "$@" | grep -q 'search(query'; then
      cat "`+filepath.Join(wd, "testdata", "search.json")+`"
    else
      cat "`+filepath.Join(wd, "testdata", "checks.json")+`"
    fi
    ;;
  *) exit 2 ;;
esac
`)

	runner, err := gh.NewExecRunner()
	require.NoError(t, err)
	client := gh.New(runner, 2)

	require.NoError(t, client.Ping(context.Background()))

	prs, err := client.ListPullRequests(context.Background(), "", 100)
	require.NoError(t, err)
	require.Len(t, prs, 3)
	assert.Equal(t, "relloyd/other", prs[0].Repo)

	checks, err := client.Checks(context.Background(), prs[1].Key())
	require.NoError(t, err)
	assert.Len(t, checks, 4)
}

func TestExecRunnerReportsStderrFromAFailedCall(t *testing.T) {
	stubGH(t, `
echo "HTTP 401: Bad credentials (https://api.github.com/graphql)" >&2
exit 1
`)

	runner, err := gh.NewExecRunner()
	require.NoError(t, err)

	_, err = gh.New(runner, 1).ListPullRequests(context.Background(), "", 10)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 401: Bad credentials")

	var cmdErr *gh.CommandError
	require.ErrorAs(t, err, &cmdErr)
	assert.Equal(t, "api", cmdErr.Args[0])
}

func TestNewExecRunnerReportsAMissingBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	_, err := gh.NewExecRunner()
	require.ErrorIs(t, err, gh.ErrNotInstalled)
}

func TestChecksSkipsAPullRequestWithoutARollup(t *testing.T) {
	stubGH(t, `echo '{"data":{"repository":{"pullRequest":{"commits":{"nodes":[{"commit":{"statusCheckRollup":null}}]}}}}}'`)

	runner, err := gh.NewExecRunner()
	require.NoError(t, err)

	checks, err := gh.New(runner, 1).Checks(context.Background(), model.Key{Repo: "a/b", Number: 1})
	require.NoError(t, err)
	assert.Empty(t, checks)
}
