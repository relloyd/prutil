package ui

import (
	"context"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	"github.com/relloyd/prutil/internal/gh"
	"github.com/relloyd/prutil/internal/handoff"
	"github.com/relloyd/prutil/internal/home"
	"github.com/relloyd/prutil/internal/model"
)

// testNow is the fixed clock every UI test renders against.
var testNow = time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)

// fakeClient serves canned pull requests and checks.
type fakeClient struct {
	mu          sync.Mutex
	prs         []model.PullRequest
	closed      []model.PullRequest
	closedShort int
	checks      map[model.Key][]model.Check
	listErr     error
	closedErr   error
	checksErr   error
	listCalls   int
	closedCalls int
	checkCalls  map[model.Key]int
	// closedPartial, when true, makes SweepClosedPullRequests behave like a
	// large organisation's sweep would: it returns only the first pull
	// request as an unexhausted partial, and FinishClosedPullRequests must be
	// called to see the rest.
	closedPartial     bool
	closedFinishCalls int
	closedFinishErr   error
	// review is what ReviewThreads returns for every pull request, which is
	// all the handoff tests need: they care about how many threads are open,
	// not about which pull request they hang off.
	review      gh.Review
	reviewErr   error
	reviewCalls int
	// snapshots is what WatchSnapshot returns, keyed by node id, and watchIDs
	// records every batch it was asked for.
	snapshots  map[string]model.Snapshot
	watchErr   error
	watchIDs   [][]string
	watchCalls int
}

func newFakeClient(prs []model.PullRequest, checks map[model.Key][]model.Check) *fakeClient {
	return &fakeClient{
		prs:        prs,
		closed:     sampleClosedPRs(),
		checks:     checks,
		checkCalls: map[model.Key]int{},
	}
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

func (f *fakeClient) SweepClosedPullRequests(_ context.Context, _ gh.ClosedOptions) (gh.ClosedResult, gh.ClosedSweepState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closedCalls++
	if f.closedErr != nil {
		return gh.ClosedResult{}, gh.ClosedSweepState{}, f.closedErr
	}
	if f.closedPartial {
		partial := f.closed[:min(1, len(f.closed))]
		return gh.ClosedResult{PRs: partial}, gh.ClosedSweepState{}, nil
	}
	return gh.ClosedResult{PRs: f.closed, Unavailable: f.closedShort}, gh.ClosedSweepState{Exhausted: true}, nil
}

func (f *fakeClient) FinishClosedPullRequests(_ context.Context, _ gh.ClosedOptions, _ gh.ClosedSweepState) (gh.ClosedResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closedFinishCalls++
	if f.closedFinishErr != nil {
		return gh.ClosedResult{}, f.closedFinishErr
	}
	return gh.ClosedResult{PRs: f.closed, Unavailable: f.closedShort}, nil
}

func (f *fakeClient) closedCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closedCalls
}

func (f *fakeClient) closedFinishCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closedFinishCalls
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

func (f *fakeClient) ReviewThreads(_ context.Context, _ model.Key) (gh.Review, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reviewCalls++
	if f.reviewErr != nil {
		return gh.Review{}, f.reviewErr
	}
	return f.review, nil
}

func (f *fakeClient) WatchSnapshot(_ context.Context, ids []string) ([]model.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.watchCalls++
	f.watchIDs = append(f.watchIDs, append([]string(nil), ids...))
	if f.watchErr != nil {
		return nil, f.watchErr
	}

	out := make([]model.Snapshot, 0, len(ids))
	for _, id := range ids {
		if snap, ok := f.snapshots[id]; ok {
			out = append(out, snap)
		}
	}
	return out, nil
}

// batches returns the node ids of every watch request so far.
func (f *fakeClient) batches() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.watchIDs...)
}

func (f *fakeClient) callsFor(key model.Key) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checkCalls[key]
}

// fakeClipboard records what the app asked to copy.
type fakeClipboard struct {
	mu   sync.Mutex
	text []string
	err  error
}

func (f *fakeClipboard) Write(text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.text = append(f.text, text)
	return nil
}

func (f *fakeClipboard) copied() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.text...)
}

// clipboardOf returns the fake clipboard newTestApp handed the app.
func clipboardOf(t *testing.T, app *App) *fakeClipboard {
	t.Helper()
	board, ok := app.clip.(*fakeClipboard)
	require.True(t, ok, "the app under test must have been given a fake clipboard")
	return board
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
			Repo: "relloyd/prutil", Number: 42, NodeID: "PR_42",
			Title:   "Add a retry to the uploader so that flaky networks stop breaking the nightly job",
			URL:     "https://github.com/relloyd/prutil/pull/42",
			HeadRef: "feat/uploader-retry", BaseRef: "main",
			CreatedAt: testNow.Add(-48 * time.Hour), UpdatedAt: testNow.Add(-2 * time.Hour),
			Mergeable: model.MergeClean, ReviewDecision: model.ReviewApproved,
			Additions: 120, Deletions: 30, ChangedFiles: 7, Comments: 4,
			Rollup: model.StatusSuccess,
		},
		{
			Repo: "relloyd/other", Number: 7, NodeID: "PR_7",
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
			Repo: "relloyd/third", Number: 9, NodeID: "PR_9",
			Title:   "Bump dependencies",
			URL:     "https://github.com/relloyd/third/pull/9",
			HeadRef: "deps/bump", BaseRef: "main",
			CreatedAt: testNow.Add(-96 * time.Hour), UpdatedAt: testNow.Add(-96 * time.Hour),
			Rollup: model.StatusUnknown,
		},
	}
}

// sampleClosedPRs returns four closed pull requests, most recently closed
// first, with two from one repository so that per-repo grouping has something
// to bite on.
func sampleClosedPRs() []model.PullRequest {
	return []model.PullRequest{
		{
			Repo: "relloyd/prutil", Number: 40,
			Title:   "Cache the check rollup between refreshes",
			URL:     "https://github.com/relloyd/prutil/pull/40",
			HeadRef: "perf/rollup-cache", BaseRef: "main",
			CreatedAt: testNow.Add(-120 * time.Hour), UpdatedAt: testNow.Add(-24 * time.Hour),
			ClosedAt: testNow.Add(-24 * time.Hour), MergedAt: testNow.Add(-24 * time.Hour),
			State:     model.PRStateMerged,
			Additions: 88, Deletions: 12, ChangedFiles: 4,
			Rollup: model.StatusSuccess,
		},
		{
			Repo: "relloyd/prutil", Number: 38,
			Title:   "Drop the unused pagination cursor",
			URL:     "https://github.com/relloyd/prutil/pull/38",
			HeadRef: "chore/cursor", BaseRef: "main",
			CreatedAt: testNow.Add(-200 * time.Hour), UpdatedAt: testNow.Add(-72 * time.Hour),
			ClosedAt:  testNow.Add(-72 * time.Hour),
			State:     model.PRStateClosed,
			Additions: 2, Deletions: 40, ChangedFiles: 2,
			Rollup: model.StatusUnknown,
		},
		{
			Repo: "relloyd/other", Number: 5,
			Title:   "Switch the loader to the new config format",
			URL:     "https://github.com/relloyd/other/pull/5",
			HeadRef: "feat/config-v2", BaseRef: "develop",
			CreatedAt: testNow.Add(-300 * time.Hour), UpdatedAt: testNow.Add(-96 * time.Hour),
			ClosedAt: testNow.Add(-96 * time.Hour), MergedAt: testNow.Add(-96 * time.Hour),
			State:     model.PRStateMerged,
			Additions: 310, Deletions: 205, ChangedFiles: 18,
			Rollup: model.StatusSuccess,
		},
		{
			Repo: "relloyd/third", Number: 8,
			Title:   "Pin the linter version",
			URL:     "https://github.com/relloyd/third/pull/8",
			HeadRef: "ci/pin-linter", BaseRef: "main",
			CreatedAt: testNow.Add(-400 * time.Hour), UpdatedAt: testNow.Add(-240 * time.Hour),
			ClosedAt: testNow.Add(-240 * time.Hour), MergedAt: testNow.Add(-240 * time.Hour),
			State:     model.PRStateMerged,
			Additions: 1, Deletions: 1, ChangedFiles: 1,
			Rollup: model.StatusSuccess,
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

// fakeDispatcher records the handoffs the app asked for and replies with a
// canned result.
type fakeDispatcher struct {
	mu     sync.Mutex
	reqs   []handoff.Request
	result handoff.Result
	err    error
	dry    bool
}

func (f *fakeDispatcher) Dispatch(_ context.Context, req handoff.Request) (handoff.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, req)
	return f.result, f.err
}

func (f *fakeDispatcher) DryRun() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dry
}

func (f *fakeDispatcher) requests() []handoff.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]handoff.Request(nil), f.reqs...)
}

// dispatcherOf returns the fake handoff dispatcher newTestApp handed the app.
func dispatcherOf(t *testing.T, app *App) *fakeDispatcher {
	t.Helper()
	got, ok := app.hand.(*fakeDispatcher)
	require.True(t, ok, "the app under test must have been given a fake dispatcher")
	return got
}

// sampleThreads returns two review threads waiting on the viewer and one the
// viewer answered themselves, which is the shape every dedup question needs.
func sampleThreads() gh.Review {
	return gh.Review{
		Viewer: "relloyd",
		Threads: []model.ReviewThread{
			{
				ID: "T1", Path: "internal/gh/client.go", Opener: "reviewer",
				Body: "This retries forever.", LatestBy: "reviewer", LatestID: "C1",
				URL: "https://github.com/relloyd/prutil/pull/42#discussion_r1",
			},
			{
				ID: "T2", Path: "README.md", Opener: "reviewer",
				Body: "Typo.", LatestBy: "reviewer", LatestID: "C2",
				URL: "https://github.com/relloyd/prutil/pull/42#discussion_r2",
			},
			{
				ID: "T3", Path: "internal/ui/app.go", Opener: "reviewer",
				Body: "Why here?", LatestBy: "relloyd", LatestID: "C3",
				URL: "https://github.com/relloyd/prutil/pull/42#discussion_r3",
			},
			{
				ID: "T4", Path: "internal/ui/view.go", Resolved: true, Opener: "reviewer",
				Body: "Done.", LatestBy: "reviewer", LatestID: "C4",
				URL: "https://github.com/relloyd/prutil/pull/42#discussion_r4",
			},
		},
	}
}

// newTestApp builds an app sized to the given terminal, with the list already
// loaded and every check cached, so tests can go straight to behaviour. Its
// clipboard, its handoff dispatcher and its application directory are all
// fakes; reach them through app.clip, dispatcherOf and app.store.
func newTestApp(t *testing.T, width, height int) (*App, *fakeClient, *fakeOpener) {
	t.Helper()

	client := newFakeClient(samplePRs(), sampleChecks())
	client.review = sampleThreads()
	client.snapshots = map[string]model.Snapshot{
		"PR_42": {NodeID: "PR_42", HeadOID: "abc", UpdatedAt: testNow, Rollup: model.StatusSuccess},
		"PR_7":  {NodeID: "PR_7", HeadOID: "def", UpdatedAt: testNow, Rollup: model.StatusFailure},
		"PR_9":  {NodeID: "PR_9", HeadOID: "ghi", UpdatedAt: testNow},
	}
	opener := &fakeOpener{}
	app := New(Config{
		Client:    client,
		Opener:    opener,
		Clipboard: &fakeClipboard{},
		Now:       func() time.Time { return testNow },
		Store:     home.OpenIn(t.TempDir()),
		State:     home.NewState(),
		Home:      fastWatch(),
		Handoff:   &fakeDispatcher{},
	})

	send(t, app, tea.WindowSizeMsg{Width: width, Height: height})
	send(t, app, prsMsg{gen: app.gen, prs: samplePRs()})

	// Settle the prefetch the list load kicked off, so tests start from a fully
	// loaded cache rather than a spinner.
	checks := sampleChecks()
	for _, pr := range app.cur().prs {
		send(t, app, checksMsg{gen: app.gen, key: pr.Key(), checks: checks[pr.Key()]})
	}
	return app, client, opener
}

// fastWatch is the configuration the UI tests build an app on. The engine's
// own tests cover the real intervals; here the point is the wiring, and a test
// has no business waiting two minutes for a tick.
func fastWatch() home.Config {
	cfg := home.DefaultConfig()
	tiny := home.Duration(time.Millisecond)
	cfg.Watch = home.WatchConfig{
		ActiveInterval: tiny, BaseInterval: tiny, MaxInterval: tiny,
		NotifiedInterval: tiny, MaxNotifiedInterval: tiny, IdleInterval: tiny,
		DormantAfter: 3, ForcePreciseEvery: 5,
	}
	return cfg
}

// advance moves the app's clock, which is what lets a test reach the watcher's
// next poll without waiting for it.
func advance(app *App, d time.Duration) {
	at := app.now().Add(d)
	app.now = func() time.Time { return at }
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

// click builds a left-button mouse click at a terminal coordinate.
func click(x, y int) tea.MouseClickMsg {
	return tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft}
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
