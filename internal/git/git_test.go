package git_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/relloyd/prutil/internal/git"
)

// fakeRunner replies to git invocations from a table keyed by the joined
// arguments, and counts what it was asked.
type fakeRunner struct {
	replies map[string]string
	fails   map[string]bool
	calls   int
}

func (f *fakeRunner) Run(_ context.Context, args ...string) ([]byte, error) {
	f.calls++
	joined := strings.Join(args, " ")
	if f.fails[joined] {
		return nil, errors.New("git " + joined + ": exit status 128")
	}
	out, ok := f.replies[joined]
	if !ok {
		return nil, errors.New("unexpected call: git " + joined)
	}
	return []byte(out + "\n"), nil
}

func checkoutRunner() *fakeRunner {
	return &fakeRunner{replies: map[string]string{
		"-C /work/prutil rev-parse --show-toplevel": "/work/prutil",
		"-C /work/prutil branch --show-current":     "feat/uploader-retry",
		"-C /work/prutil remote get-url origin":     "git@github.com:relloyd/prutil.git",
	}}
}

func TestIdentifyReadsTheRepositoryAndBranchOutOfACheckout(t *testing.T) {
	got := git.New(checkoutRunner()).Identify(context.Background(), "/work/prutil")

	assert.Equal(t, "/work/prutil", got.Root)
	assert.Equal(t, "relloyd/prutil", got.Repo)
	assert.Equal(t, "feat/uploader-retry", got.Branch)
}

func TestADirectoryThatIsNotAWorkingTreeIsAnOrdinaryEmptyAnswer(t *testing.T) {
	runner := &fakeRunner{
		replies: map[string]string{},
		fails:   map[string]bool{"-C /tmp rev-parse --show-toplevel": true},
	}

	got := git.New(runner).Identify(context.Background(), "/tmp")
	assert.Equal(t, "", got.Repo, "an agent sitting outside a repository is simply not a candidate")
	assert.Equal(t, 1, runner.calls, "there is nothing left to ask once the first question fails")
}

func TestACheckoutWithNoOriginStillReportsItsBranch(t *testing.T) {
	runner := checkoutRunner()
	runner.fails = map[string]bool{"-C /work/prutil remote get-url origin": true}

	got := git.New(runner).Identify(context.Background(), "/work/prutil")
	assert.Equal(t, "feat/uploader-retry", got.Branch)
	assert.Empty(t, got.Repo)
}

func TestAnEmptyDirectoryIsNotAskedAbout(t *testing.T) {
	runner := checkoutRunner()
	assert.Equal(t, git.Checkout{}, git.New(runner).Identify(context.Background(), ""))
	assert.Zero(t, runner.calls)
}

func TestASecondLookAtTheSameDirectoryComesFromTheCache(t *testing.T) {
	runner := checkoutRunner()
	client := git.New(runner)

	first := client.Identify(context.Background(), "/work/prutil")
	second := client.Identify(context.Background(), "/work/prutil")

	assert.Equal(t, first, second)
	assert.Equal(t, 3, runner.calls, "three reads, once, however often the answer is wanted")
}

func TestParseRemoteHandlesTheShapesGitHandsOut(t *testing.T) {
	for _, tc := range []struct {
		name, url, want string
	}{
		{"https", "https://github.com/relloyd/prutil.git", "relloyd/prutil"},
		{"https without suffix", "https://github.com/relloyd/prutil", "relloyd/prutil"},
		{"scp", "git@github.com:relloyd/prutil.git", "relloyd/prutil"},
		{"ssh", "ssh://git@github.com/relloyd/prutil.git", "relloyd/prutil"},
		{"enterprise", "https://github.example.com/team/repo.git", "team/repo"},
		{"trailing slash", "https://github.com/relloyd/prutil/", "relloyd/prutil"},
		{"padded", "  git@github.com:relloyd/prutil.git\n", "relloyd/prutil"},
		{"empty", "", ""},
		{"nonsense", "not-a-url", ""},
		{"scp with no path", "git@github.com:prutil", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, git.ParseRemote(tc.url))
		})
	}
}
