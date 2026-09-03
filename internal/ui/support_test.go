package ui

import (
	"context"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	"github.com/relloyd/prutil/internal/model"
)

// testNow is the fixed clock every UI test renders against.
var testNow = time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)

// fakeClient serves canned pull requests and checks.
type fakeClient struct {
	mu         sync.Mutex
	prs        []model.PullRequest
	checks     map[model.Key][]model.Check
	listErr    error
	checksErr  error
	listCalls  int
	checkCalls map[model.Key]int
}

func newFakeClient(prs []model.PullRequest, checks map[model.Key][]model.Check) *fakeClient {
	return &fakeClient{prs: prs, checks: checks, checkCalls: map[model.Key]int{}}
}

func (f *fakeClient) Ping(context.Context) error { return nil }

func (f *fakeClient) ListPullRequests(_ context.Context, _ string, _ int) ([]model.PullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.prs, nil
}

func (f *fakeClient) Checks(_ context.Context, key model.Key) ([]model.Check, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checkCalls[key]++
	if f.checksErr != nil {
		return nil, f.checksErr
	}
	return f.checks[key], nil
}

func (f *fakeClient) callsFor(key model.Key) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checkCalls[key]
}

// fakeOpener records the URLs the app asked to open.
type fakeOpener struct {
	mu   sync.Mutex
	urls []string
	err  error
}

func (f *fakeOpener) Open(url string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.urls = append(f.urls, url)
	return nil
}

func (f *fakeOpener) opened() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.urls...)
}

// samplePRs returns three pull requests, newest first, matching the order the
// client would have produced.
func samplePRs() []model.PullRequest {
	return []model.PullRequest{
		{
			Repo: "relloyd/prutil", Number: 42,
			Title:   "Add a retry to the uploader so that flaky networks stop breaking the nightly job",
			URL:     "https://github.com/relloyd/prutil/pull/42",
			HeadRef: "feat/uploader-retry", BaseRef: "main",
			CreatedAt: testNow.Add(-48 * time.Hour), UpdatedAt: testNow.Add(-2 * time.Hour),
			Mergeable: model.MergeClean, ReviewDecision: model.ReviewApproved,
			Additions: 120, Deletions: 30, ChangedFiles: 7, Comments: 4,
			Rollup: model.StatusSuccess,
		},
		{
			Repo: "relloyd/other", Number: 7,
			Title:   "Rework the config loader",
			URL:     "https://github.com/relloyd/other/pull/7",
			HeadRef: "chore/config", BaseRef: "develop",
			CreatedAt: testNow.Add(-72 * time.Hour), UpdatedAt: testNow.Add(-71 * time.Hour),
			IsDraft:   true,
			Mergeable: model.MergeConflicting, ReviewDecision: model.ReviewChangesRequested,
			Additions: 3, Deletions: 1, ChangedFiles: 1,
			Rollup: model.StatusFailure,
		},
		{
			Repo: "relloyd/third", Number: 9,
			Title:   "Bump dependencies",
			URL:     "https://github.com/relloyd/third/pull/9",
			HeadRef: "deps/bump", BaseRef: "main",
			CreatedAt: testNow.Add(-96 * time.Hour), UpdatedAt: testNow.Add(-96 * time.Hour),
			Rollup: model.StatusUnknown,
		},
	}
}

// sampleChecks returns the checks belonging to the first sample pull request.
func sampleChecks() map[model.Key][]model.Check {
	return map[model.Key][]model.Check{
		{Repo: "relloyd/prutil", Number: 42}: {
			{
				Name: "test", Workflow: "CI", Status: model.StatusSuccess,
				URL:       "https://github.com/relloyd/prutil/actions/runs/1/job/1",
				StartedAt: testNow.Add(-10 * time.Minute), CompletedAt: testNow.Add(-9 * time.Minute),
			},
			{
				Name: "lint", Workflow: "CI", Status: model.StatusFailure,
				URL:       "https://github.com/relloyd/prutil/actions/runs/1/job/2",
				StartedAt: testNow.Add(-10 * time.Minute), CompletedAt: testNow.Add(-10 * time.Minute).Add(12 * time.Second),
			},
			{
				Name: "build", Workflow: "CI", Status: model.StatusPending,
				URL:       "https://github.com/relloyd/prutil/actions/runs/1/job/3",
				StartedAt: testNow.Add(-3 * time.Minute),
			},
		},
	}
}

// newTestApp builds an app sized to the given terminal, with the list already
// loaded and every check cached, so tests can go straight to behaviour.
func newTestApp(t *testing.T, width, height int) (*App, *fakeClient, *fakeOpener) {
	t.Helper()

	client := newFakeClient(samplePRs(), sampleChecks())
	opener := &fakeOpener{}
	app := New(Config{
		Client: client,
		Opener: opener,
		Now:    func() time.Time { return testNow },
	})

	send(t, app, tea.WindowSizeMsg{Width: width, Height: height})
	send(t, app, prsMsg{gen: app.gen, prs: samplePRs()})

	// Settle the prefetch the list load kicked off, so tests start from a fully
	// loaded cache rather than a spinner.
	checks := sampleChecks()
	for _, pr := range app.prs {
		send(t, app, checksMsg{gen: app.gen, key: pr.Key(), checks: checks[pr.Key()]})
	}
	return app, client, opener
}

// send delivers one message and returns the command it produced.
func send(t *testing.T, app *App, msg tea.Msg) tea.Cmd {
	t.Helper()
	next, cmd := app.Update(msg)
	require.Same(t, app, next, "the app updates in place")
	return cmd
}

// press builds the key message for a keystroke such as "j", "enter" or
// "ctrl+c".
func press(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "left":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	default:
		runes := []rune(s)
		return tea.KeyPressMsg{Code: runes[0], Text: s}
	}
}

// drain runs a command, following batches, and returns every message produced.
// Commands that would block on a timer are skipped.
func drain(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, drain(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}
