package handoff_test

import (
	"context"
	"errors"
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
	gets      []herdr.Agent
	getErr    error
	promptErr error

	prompted []string
	texts    []string
	toasts   []string
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

// dispatcherFor builds a dispatcher whose waits cost nothing, and reports how
// many times it waited.
func dispatcherFor(t *testing.T, control *fakeHerdr, checkouts fakeGit, tune func(*home.Config)) (*handoff.Dispatcher, *int) {
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
