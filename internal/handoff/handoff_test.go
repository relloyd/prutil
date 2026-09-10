package handoff_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/relloyd/prutil/internal/git"
	"github.com/relloyd/prutil/internal/handoff"
	"github.com/relloyd/prutil/internal/herdr"
	"github.com/relloyd/prutil/internal/home"
	"github.com/relloyd/prutil/internal/model"
)

// samplePR is the pull request every test hands over.
func samplePR() model.PullRequest {
	return model.PullRequest{
		Repo: "relloyd/prutil", Number: 42,
		Title:   "Add a retry to the uploader",
		URL:     "https://github.com/relloyd/prutil/pull/42",
		HeadRef: "feat/uploader-retry", BaseRef: "main",
	}
}

// fakeHerdr stands in for a running herdr server.
type fakeHerdr struct {
	agents    []herdr.Agent
	agentsErr error
	// gets is consumed one reply per call to Agent, which is how a test makes
	// a working agent settle after a given number of looks. The last entry is
	// repeated once the queue runs dry.
	gets        []herdr.Agent
	getErr      error
	promptErr   error
	worktrees   [][]herdr.Worktree
	worktreeErr error
	create      herdr.WorktreeSession
	createErr   error
	open        herdr.WorktreeSession
	openErr     error
	start       herdr.Agent
	startErr    error

	prompted []string
	texts    []string
	toasts   []string
	started  []string
	opened   []string
	created  []string
}

func (f *fakeHerdr) Agents(context.Context) ([]herdr.Agent, error) {
	return f.agents, f.agentsErr
}

func (f *fakeHerdr) Agent(_ context.Context, target string) (herdr.Agent, error) {
	if f.getErr != nil {
		return herdr.Agent{}, f.getErr
	}
	if len(f.gets) == 0 {
		return herdr.Agent{PaneID: target, Status: herdr.StatusWorking}, nil
	}
	got := f.gets[0]
	if len(f.gets) > 1 {
		f.gets = f.gets[1:]
	}
	return got, nil
}

func (f *fakeHerdr) Worktrees(context.Context, string) ([]herdr.Worktree, error) {
	if f.worktreeErr != nil {
		return nil, f.worktreeErr
	}
	if len(f.worktrees) == 0 {
		return nil, nil
	}
	worktrees := f.worktrees[0]
	if len(f.worktrees) > 1 {
		f.worktrees = f.worktrees[1:]
	}
	return worktrees, nil
}

func (f *fakeHerdr) CreateWorktree(_ context.Context, root, branch, label string) (herdr.WorktreeSession, error) {
	f.created = append(f.created, root+"|"+branch+"|"+label)
	return f.create, f.createErr
}

func (f *fakeHerdr) OpenWorktree(_ context.Context, root, path, branch, label string) (herdr.WorktreeSession, error) {
	f.opened = append(f.opened, root+"|"+path+"|"+branch+"|"+label)
	return f.open, f.openErr
}

func (f *fakeHerdr) StartAgent(_ context.Context, name, kind, pane string, _ time.Duration) (herdr.Agent, error) {
	f.started = append(f.started, name+"|"+kind+"|"+pane)
	return f.start, f.startErr
}

func (f *fakeHerdr) Prompt(_ context.Context, target, text string) error {
	if f.promptErr != nil {
		return f.promptErr
	}
	f.prompted = append(f.prompted, target)
	f.texts = append(f.texts, text)
	return nil
}

func (f *fakeHerdr) Notify(_ context.Context, title, body string) error {
	f.toasts = append(f.toasts, title+" | "+body)
	return nil
}

// fakeGit answers for the directories a test set up, and reports every other
// directory as no repository at all.
type fakeGit map[string]git.Checkout

func (f fakeGit) Identify(_ context.Context, dir string) git.Checkout { return f[dir] }

type fakeResolver struct {
	checkout git.Checkout
	err      error
	repos    []string
}

func (f *fakeResolver) Resolve(_ context.Context, repo string) (git.Checkout, error) {
	f.repos = append(f.repos, repo)
	return f.checkout, f.err
}

type fakeFetcher struct {
	err   error
	calls []string
}

func (f *fakeFetcher) FetchPullRequest(_ context.Context, root string, number int, branch string) error {
	f.calls = append(f.calls, fmt.Sprintf("%s|%d|%s", root, number, branch))
	return f.err
}

// dispatcherFor builds a dispatcher whose waits cost nothing, and reports how
// many times it waited.
func dispatcherFor(t *testing.T, control *fakeHerdr, checkouts fakeGit, tune func(*home.Config)) (*handoff.Dispatcher, *int) {
	return dispatcherWithProvision(t, control, checkouts, nil, nil, tune)
}

// dispatcherWithProvision builds a dispatcher with optional local repository
// seams for tests that exercise manual workspace provisioning.
func dispatcherWithProvision(t *testing.T, control *fakeHerdr, checkouts fakeGit, repos handoff.RepositoryResolver, fetch git.PullRequestFetcher, tune func(*home.Config)) (*handoff.Dispatcher, *int) {
	t.Helper()

	cfg := home.DefaultConfig()
	cfg.Herdr.Skill = "pr-triage"
	if tune != nil {
		tune(&cfg)
	}

	waits := 0
	return handoff.New(handoff.Options{
		Herdr:    control,
		Git:      checkouts,
		Repos:    repos,
		Fetch:    fetch,
		Config:   cfg,
		SelfPane: "w1:p3",
		Sleep: func(ctx context.Context, _ time.Duration) bool {
			waits++
			return ctx.Err() == nil
		},
	}), &waits
}

func request() handoff.Request {
	return handoff.Request{
		PR:              samplePR(),
		UnresolvedCount: 3,
		NewCount:        2,
		Threads:         map[string]string{"T1": "C1"},
	}
}

func TestTheAgentOnTheHeadBranchIsPreferredOverOneMerelyInTheRepository(t *testing.T) {
	control := &fakeHerdr{agents: []herdr.Agent{
		{Kind: "claude", Status: herdr.StatusIdle, CWD: "/work/main", PaneID: "w2:p1"},
		{Kind: "claude", Status: herdr.StatusIdle, CWD: "/work/retry", PaneID: "w3:p1"},
	}}
	checkouts := fakeGit{
		"/work/main":  {Repo: "relloyd/prutil", Branch: "main"},
		"/work/retry": {Repo: "relloyd/prutil", Branch: "feat/uploader-retry"},
	}

	dispatcher, _ := dispatcherFor(t, control, checkouts, nil)
	res, err := dispatcher.Dispatch(context.Background(), request())

	require.NoError(t, err)
	assert.Equal(t, home.OutcomeSent, res.Outcome)
	assert.Equal(t, []string{"w3:p1"}, control.prompted)
	assert.Equal(t, "/pr-triage https://github.com/relloyd/prutil/pull/42", control.texts[0])
}

func TestAManualHandoffWithoutAnAgentRequiresAConfiguredKindBeforeProvisioning(t *testing.T) {
	control := &fakeHerdr{}
	repos := &fakeResolver{checkout: git.Checkout{Root: "/work/prutil", Repo: "relloyd/prutil"}}
	fetch := &fakeFetcher{}
	dispatcher, _ := dispatcherWithProvision(t, control, fakeGit{}, repos, fetch, nil)
	req := request()
	req.AllowProvision = true

	res, err := dispatcher.Dispatch(context.Background(), req)

	require.ErrorIs(t, err, handoff.ErrAgentKindRequired)
	assert.Equal(t, home.OutcomeFailed, res.Outcome)
	assert.Empty(t, repos.repos)
	assert.Empty(t, fetch.calls)
	assert.Empty(t, control.created)
}

func TestAManualHandoffCreatesAWorkspaceAndStartsAnAgentWhenNoneExists(t *testing.T) {
	control := &fakeHerdr{
		create: herdr.WorktreeSession{WorkspaceID: "w8", TabID: "w8:t1", RootPaneID: "w8:p1"},
		start:  herdr.Agent{Kind: "claude", Status: herdr.StatusIdle, CWD: "/work/prutil", PaneID: "w8:p1", Name: "pr-relloyd-prutil-42"},
	}
	repos := &fakeResolver{checkout: git.Checkout{Root: "/work/prutil", Repo: "relloyd/prutil"}}
	fetch := &fakeFetcher{}
	dispatcher, _ := dispatcherWithProvision(t, control, fakeGit{}, repos, fetch, func(cfg *home.Config) {
		cfg.Herdr.AgentKind = "claude"
	})
	req := request()
	req.AllowProvision = true

	res, err := dispatcher.Dispatch(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, home.OutcomeSent, res.Outcome)
	assert.True(t, res.Provisioned)
	assert.Equal(t, "w8", res.Workspace)
	assert.Equal(t, "w8:t1", res.Tab)
	assert.Equal(t, []string{"relloyd/prutil"}, repos.repos)
	assert.Equal(t, []string{"/work/prutil|42|prutil/relloyd-prutil-42"}, fetch.calls)
	assert.Equal(t, []string{"/work/prutil|prutil/relloyd-prutil-42|relloyd/prutil#42"}, control.created)
	require.Len(t, control.started, 1)
	assert.Contains(t, control.started[0], "|claude|w8:p1")
	assert.Equal(t, []string{"w8:p1"}, control.prompted)
}

func TestAManualHandoffReopensAnExistingMatchingWorktree(t *testing.T) {
	control := &fakeHerdr{
		worktrees: [][]herdr.Worktree{{{Path: "/work/prutil-42", Branch: "prutil/relloyd-prutil-42"}}},
		open:      herdr.WorktreeSession{WorkspaceID: "w8", TabID: "w8:t1", RootPaneID: "w8:p1"},
		start:     herdr.Agent{Kind: "claude", Status: herdr.StatusIdle, PaneID: "w8:p1"},
	}
	repos := &fakeResolver{checkout: git.Checkout{Root: "/work/prutil", Repo: "relloyd/prutil"}}
	fetch := &fakeFetcher{}
	dispatcher, _ := dispatcherWithProvision(t, control, fakeGit{}, repos, fetch, func(cfg *home.Config) {
		cfg.Herdr.AgentKind = "claude"
	})
	req := request()
	req.AllowProvision = true

	_, err := dispatcher.Dispatch(context.Background(), req)

	require.NoError(t, err)
	assert.Empty(t, control.created)
	assert.Equal(t, []string{"/work/prutil|/work/prutil-42|prutil/relloyd-prutil-42|relloyd/prutil#42"}, control.opened)
}

func TestAManualProvisioningNeverMutatesDuringADryRun(t *testing.T) {
	control := &fakeHerdr{}
	repos := &fakeResolver{checkout: git.Checkout{Root: "/work/prutil", Repo: "relloyd/prutil"}}
	fetch := &fakeFetcher{}
	dispatcher, _ := dispatcherWithProvision(t, control, fakeGit{}, repos, fetch, func(cfg *home.Config) {
		cfg.Herdr.AgentKind = "claude"
		cfg.Herdr.DryRun = true
	})
	req := request()
	req.AllowProvision = true

	res, err := dispatcher.Dispatch(context.Background(), req)

	require.NoError(t, err)
	assert.Equal(t, home.OutcomeDryRun, res.Outcome)
	assert.False(t, res.Provisioned)
	assert.Equal(t, []string{"relloyd/prutil"}, repos.repos)
	assert.Empty(t, fetch.calls)
	assert.Empty(t, control.created)
	assert.Empty(t, control.opened)
	assert.Empty(t, control.started)
	assert.Empty(t, control.prompted)
}

func TestAManualProvisioningReportsAFetchFailureWithoutCreatingAWorkspace(t *testing.T) {
	control := &fakeHerdr{}
	repos := &fakeResolver{checkout: git.Checkout{Root: "/work/prutil", Repo: "relloyd/prutil"}}
	fetch := &fakeFetcher{err: errors.New("fetch refused")}
	dispatcher, _ := dispatcherWithProvision(t, control, fakeGit{}, repos, fetch, func(cfg *home.Config) {
		cfg.Herdr.AgentKind = "claude"
	})
	req := request()
	req.AllowProvision = true

	res, err := dispatcher.Dispatch(context.Background(), req)

	require.ErrorIs(t, err, fetch.err)
	assert.Equal(t, home.OutcomeFailed, res.Outcome)
	assert.Empty(t, control.created)
	assert.Empty(t, control.started)
}

func TestAnAgentOnTheWrongBranchIsStillUsedAndToldSo(t *testing.T) {
	control := &fakeHerdr{agents: []herdr.Agent{
		{Kind: "claude", Status: herdr.StatusIdle, CWD: "/work/main", PaneID: "w2:p1"},
	}}
	checkouts := fakeGit{"/work/main": {Repo: "relloyd/prutil", Branch: "main"}}

	dispatcher, _ := dispatcherFor(t, control, checkouts, func(cfg *home.Config) {
		cfg.Herdr.Skill = ""
	})
	res, err := dispatcher.Dispatch(context.Background(), request())

	require.NoError(t, err)
	assert.Equal(t, home.OutcomeSent, res.Outcome)
	assert.Contains(t, control.texts[0], "is on main, not this pull request's feat/uploader-retry")
}

func TestPrutilNeverHandsWorkToTheTerminalItIsRunningIn(t *testing.T) {
	control := &fakeHerdr{agents: []herdr.Agent{
		{Kind: "claude", Status: herdr.StatusIdle, CWD: "/work/retry", PaneID: "w1:p3"},
	}}
	checkouts := fakeGit{"/work/retry": {Repo: "relloyd/prutil", Branch: "feat/uploader-retry"}}

	dispatcher, _ := dispatcherFor(t, control, checkouts, nil)
	_, err := dispatcher.Dispatch(context.Background(), request())

	require.ErrorIs(t, err, handoff.ErrNoAgent)
	assert.Empty(t, control.prompted)
}

func TestOnlyTheConfiguredKindOfAgentIsConsidered(t *testing.T) {
	control := &fakeHerdr{agents: []herdr.Agent{
		{Kind: "codex", Status: herdr.StatusIdle, CWD: "/work/retry", PaneID: "w2:p1"},
	}}
	checkouts := fakeGit{"/work/retry": {Repo: "relloyd/prutil", Branch: "feat/uploader-retry"}}

	dispatcher, _ := dispatcherFor(t, control, checkouts, func(cfg *home.Config) {
		cfg.Herdr.AgentKind = "claude"
	})
	_, err := dispatcher.Dispatch(context.Background(), request())

	require.ErrorIs(t, err, handoff.ErrNoAgent)
}

func TestAnAgentInAnotherRepositoryIsNotACandidate(t *testing.T) {
	control := &fakeHerdr{agents: []herdr.Agent{
		{Kind: "claude", Status: herdr.StatusIdle, CWD: "/work/other", PaneID: "w2:p1"},
	}}
	checkouts := fakeGit{"/work/other": {Repo: "relloyd/other", Branch: "feat/uploader-retry"}}

	dispatcher, _ := dispatcherFor(t, control, checkouts, nil)
	res, err := dispatcher.Dispatch(context.Background(), request())

	require.ErrorIs(t, err, handoff.ErrNoAgent)
	assert.Equal(t, home.OutcomeNoAgent, res.Outcome)
	require.Len(t, control.toasts, 1, "the reader is told even though no agent was")
	assert.Contains(t, control.toasts[0], "2 new review comments")
}

func TestABlockedAgentIsNeverPrompted(t *testing.T) {
	control := &fakeHerdr{agents: []herdr.Agent{
		{Kind: "claude", Status: herdr.StatusBlocked, CWD: "/work/retry", PaneID: "w2:p1"},
	}}
	checkouts := fakeGit{"/work/retry": {Repo: "relloyd/prutil", Branch: "feat/uploader-retry"}}

	dispatcher, _ := dispatcherFor(t, control, checkouts, nil)
	res, err := dispatcher.Dispatch(context.Background(), request())

	require.ErrorIs(t, err, handoff.ErrBlocked)
	assert.Equal(t, home.OutcomeBlocked, res.Outcome)
	assert.Empty(t, control.prompted)
}

func TestAWorkingAgentIsWaitedForAndThenPrompted(t *testing.T) {
	control := &fakeHerdr{
		agents: []herdr.Agent{
			{Kind: "claude", Status: herdr.StatusWorking, CWD: "/work/retry", PaneID: "w2:p1"},
		},
		gets: []herdr.Agent{
			{Kind: "claude", Status: herdr.StatusWorking, CWD: "/work/retry", PaneID: "w2:p1"},
			{Kind: "claude", Status: herdr.StatusDone, CWD: "/work/retry", PaneID: "w2:p1"},
		},
	}
	checkouts := fakeGit{"/work/retry": {Repo: "relloyd/prutil", Branch: "feat/uploader-retry"}}

	dispatcher, waits := dispatcherFor(t, control, checkouts, nil)
	res, err := dispatcher.Dispatch(context.Background(), request())

	require.NoError(t, err)
	assert.Equal(t, home.OutcomeSent, res.Outcome)
	assert.Equal(t, 2, *waits)
	assert.Equal(t, 20*time.Second, res.Waited, "two ten-second looks")
	assert.Equal(t, []string{"w2:p1"}, control.prompted)
}

func TestAnAgentThatNeverSettlesGivesUpAtTheConfiguredBudget(t *testing.T) {
	control := &fakeHerdr{agents: []herdr.Agent{
		{Kind: "claude", Status: herdr.StatusWorking, CWD: "/work/retry", PaneID: "w2:p1"},
	}}
	checkouts := fakeGit{"/work/retry": {Repo: "relloyd/prutil", Branch: "feat/uploader-retry"}}

	dispatcher, waits := dispatcherFor(t, control, checkouts, func(cfg *home.Config) {
		cfg.Herdr.WaitForIdle = home.Duration(30 * time.Second)
	})
	res, err := dispatcher.Dispatch(context.Background(), request())

	require.ErrorIs(t, err, handoff.ErrStillWorking)
	assert.Equal(t, home.OutcomeFailed, res.Outcome)
	assert.Equal(t, 3, *waits, "a thirty second budget is three ten second looks")
	assert.Empty(t, control.prompted)
}

func TestAnAgentThatExitsWhilePrutilWaitsIsReportedAsGone(t *testing.T) {
	control := &fakeHerdr{
		agents: []herdr.Agent{
			{Kind: "claude", Status: herdr.StatusWorking, CWD: "/work/retry", PaneID: "w2:p1"},
		},
		getErr: &herdr.APIError{Code: herdr.CodeAgentNotFound, Message: "gone"},
	}
	checkouts := fakeGit{"/work/retry": {Repo: "relloyd/prutil", Branch: "feat/uploader-retry"}}

	dispatcher, _ := dispatcherFor(t, control, checkouts, nil)
	_, err := dispatcher.Dispatch(context.Background(), request())

	require.ErrorIs(t, err, handoff.ErrNoAgent)
}

func TestADryRunRendersThePromptWithoutSendingIt(t *testing.T) {
	control := &fakeHerdr{agents: []herdr.Agent{
		{Kind: "claude", Status: herdr.StatusIdle, CWD: "/work/retry", PaneID: "w2:p1"},
	}}
	checkouts := fakeGit{"/work/retry": {Repo: "relloyd/prutil", Branch: "feat/uploader-retry"}}

	dispatcher, _ := dispatcherFor(t, control, checkouts, func(cfg *home.Config) {
		cfg.Herdr.DryRun = true
	})
	require.True(t, dispatcher.DryRun())

	res, err := dispatcher.Dispatch(context.Background(), request())
	require.NoError(t, err)
	assert.Equal(t, home.OutcomeDryRun, res.Outcome)
	assert.Equal(t, "/pr-triage https://github.com/relloyd/prutil/pull/42", res.Prompt)
	assert.Empty(t, control.prompted, "a dry run sends nothing")
}

func TestASubmissionRefusedBecauseTheAgentBlockedIsRecordedAsBlocked(t *testing.T) {
	control := &fakeHerdr{
		agents: []herdr.Agent{
			{Kind: "claude", Status: herdr.StatusIdle, CWD: "/work/retry", PaneID: "w2:p1"},
		},
		promptErr: &herdr.APIError{Code: herdr.CodeAgentBlocked, Message: "agent is blocked"},
	}
	checkouts := fakeGit{"/work/retry": {Repo: "relloyd/prutil", Branch: "feat/uploader-retry"}}

	dispatcher, _ := dispatcherFor(t, control, checkouts, nil)
	res, err := dispatcher.Dispatch(context.Background(), request())

	require.Error(t, err)
	assert.Equal(t, home.OutcomeBlocked, res.Outcome, "the agent reached a dialog between the look and the send")
}

func TestToastsAreSilencedByConfiguration(t *testing.T) {
	control := &fakeHerdr{}
	dispatcher, _ := dispatcherFor(t, control, fakeGit{}, func(cfg *home.Config) {
		cfg.Herdr.Toast = false
	})

	_, err := dispatcher.Dispatch(context.Background(), request())
	require.ErrorIs(t, err, handoff.ErrNoAgent)
	assert.Empty(t, control.toasts)
}

func TestAHerdrThatCannotBeReachedIsReportedRatherThanTreatedAsEmpty(t *testing.T) {
	boom := errors.New("no herdr server is answering")
	control := &fakeHerdr{agentsErr: boom}

	dispatcher, _ := dispatcherFor(t, control, fakeGit{}, nil)
	res, err := dispatcher.Dispatch(context.Background(), request())

	require.ErrorIs(t, err, boom)
	assert.Equal(t, home.OutcomeFailed, res.Outcome)
}

func TestAContextThatEndsUnderTheWaitIsAFailureRatherThanABlockedAgent(t *testing.T) {
	control := &fakeHerdr{agents: []herdr.Agent{
		{Kind: "claude", Status: herdr.StatusWorking, CWD: "/work/retry", PaneID: "w2:p1"},
	}}
	checkouts := fakeGit{"/work/retry": {Repo: "relloyd/prutil", Branch: "feat/uploader-retry"}}

	dispatcher, _ := dispatcherFor(t, control, checkouts, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := dispatcher.Dispatch(ctx, request())
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, home.OutcomeFailed, res.Outcome,
		"an agent that prutil stopped waiting for was never blocked")
}

func TestAnAgentThatDisappearsIsRecordedAsMissingRatherThanBlocked(t *testing.T) {
	control := &fakeHerdr{
		agents: []herdr.Agent{
			{Kind: "claude", Status: herdr.StatusWorking, CWD: "/work/retry", PaneID: "w2:p1"},
		},
		getErr: &herdr.APIError{Code: herdr.CodeAgentNotFound, Message: "gone"},
	}
	checkouts := fakeGit{"/work/retry": {Repo: "relloyd/prutil", Branch: "feat/uploader-retry"}}

	dispatcher, _ := dispatcherFor(t, control, checkouts, nil)
	res, _ := dispatcher.Dispatch(context.Background(), request())
	assert.Equal(t, home.OutcomeNoAgent, res.Outcome)
}

func TestTheToastStillFiresWhenTheHandoffsOwnContextHasRunOut(t *testing.T) {
	control := &fakeHerdr{agents: []herdr.Agent{
		{Kind: "claude", Status: herdr.StatusWorking, CWD: "/work/retry", PaneID: "w2:p1"},
	}}
	checkouts := fakeGit{"/work/retry": {Repo: "relloyd/prutil", Branch: "feat/uploader-retry"}}

	dispatcher, _ := dispatcherFor(t, control, checkouts, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := dispatcher.Dispatch(ctx, request())
	require.Error(t, err)
	require.Len(t, control.toasts, 1,
		"a toast is what the reader gets when the handoff did not happen, so it cannot share its fate")
}
