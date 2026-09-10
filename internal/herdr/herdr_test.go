package herdr_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/relloyd/prutil/internal/herdr"
	"github.com/relloyd/prutil/internal/run"
)

// agentListJSON is a real reply from herdr 0.8.2, trimmed to the fields prutil
// reads. The extra keys are kept on purpose: the decoder has to ignore what it
// does not know rather than fail on it.
const agentListJSON = `{"id":"cli:agent:list","result":{"type":"agent_list","agents":[
  {"agent":"claude","agent_status":"working","cwd":"/Users/rl/prutil","focused":true,
   "foreground_cwd":"/Users/rl/prutil","pane_id":"w1:p3","revision":15,"state_change_seq":6,
   "tab_id":"w1:t3","terminal_id":"term_65ae","terminal_title":"◐ prutil",
   "terminal_title_stripped":"prutil","workspace_id":"w1"},
  {"agent":"codex","agent_status":"idle","cwd":"/Users/rl/other","pane_id":"w2:p1",
   "tab_id":"w2:t1","workspace_id":"w2","name":"reviewer"}]}}`

// fakeRunner replies to herdr invocations from a table keyed by the joined
// arguments, and records what it was asked.
type fakeRunner struct {
	replies map[string]reply
	calls   [][]string
}

type reply struct {
	out string
	err error
}

var _ herdr.Controller = (*herdr.Client)(nil)

func (f *fakeRunner) Run(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	got, ok := f.replies[strings.Join(args, " ")]
	if !ok {
		return nil, errors.New("unexpected call: herdr " + strings.Join(args, " "))
	}
	return []byte(got.out), got.err
}

// serverError is how the herdr CLI reports a refusal: the envelope on standard
// error, and exit status one.
func serverError(code, message string) error {
	return &run.Error{
		Bin:    herdr.Binary,
		Stderr: `{"error":{"code":"` + code + `","message":"` + message + `"},"id":"cli:agent:get"}`,
		Err:    errors.New("exit status 1"),
	}
}

func TestAgentsDecodesTheListingAndIgnoresFieldsPrutilDoesNotRead(t *testing.T) {
	runner := &fakeRunner{replies: map[string]reply{"agent list": {out: agentListJSON}}}

	agents, err := herdr.New(runner).Agents(context.Background())
	require.NoError(t, err)
	require.Len(t, agents, 2)

	assert.Equal(t, "claude", agents[0].Kind)
	assert.Equal(t, herdr.StatusWorking, agents[0].Status)
	assert.Equal(t, "w1:p3", agents[0].Target())
	assert.False(t, agents[0].Settled())

	assert.Equal(t, "reviewer", agents[1].Name)
	assert.True(t, agents[1].Settled())
}

func TestAnAgentIsAddressedByItsPaneRatherThanItsName(t *testing.T) {
	named := herdr.Agent{Name: "reviewer", PaneID: "w2:p1"}
	assert.Equal(t, "w2:p1", named.Target(), "a pane id is always present and always unique")

	unnamed := herdr.Agent{Name: "reviewer"}
	assert.Equal(t, "reviewer", unnamed.Target())
}

func TestTheForegroundDirectoryWinsOverTheOneThePaneStartedIn(t *testing.T) {
	assert.Equal(t, "/now", herdr.Agent{CWD: "/then", ForegroundCWD: "/now"}.Dir())
	assert.Equal(t, "/then", herdr.Agent{CWD: "/then"}.Dir())
}

func TestDoneCountsAsReadyForWorkAndUnknownDoesNot(t *testing.T) {
	assert.True(t, herdr.Agent{Status: herdr.StatusIdle}.Settled())
	assert.True(t, herdr.Agent{Status: herdr.StatusDone}.Settled())
	assert.False(t, herdr.Agent{Status: herdr.StatusUnknown}.Settled(),
		"unknown means herdr will not classify the agent, not that it finished")
	assert.False(t, herdr.Agent{Status: herdr.StatusBlocked}.Settled())
}

func TestAServerErrorOnStandardErrorBecomesATypedCode(t *testing.T) {
	runner := &fakeRunner{replies: map[string]reply{
		"agent get nope": {err: serverError(herdr.CodeAgentNotFound, "agent target nope not found")},
	}}

	_, err := herdr.New(runner).Agent(context.Background(), "nope")
	require.Error(t, err)
	assert.Equal(t, herdr.CodeAgentNotFound, herdr.Code(err))
	assert.Contains(t, err.Error(), "agent target nope not found")
}

func TestABlockedAgentIsReportedByItsOwnCodeSoItIsNeverPrompted(t *testing.T) {
	runner := &fakeRunner{replies: map[string]reply{
		"agent prompt w1:p3 hello": {err: serverError(herdr.CodeAgentBlocked, "agent is blocked")},
	}}

	err := herdr.New(runner).Prompt(context.Background(), "w1:p3", "hello")
	require.Error(t, err)
	assert.Equal(t, herdr.CodeAgentBlocked, herdr.Code(err))
}

func TestAFailureThatIsNotAServerErrorIsPassedThroughUntouched(t *testing.T) {
	boom := errors.New("herdr: exec format error")
	runner := &fakeRunner{replies: map[string]reply{"agent list": {err: boom}}}

	_, err := herdr.New(runner).Agents(context.Background())
	require.ErrorIs(t, err, boom)
	assert.Empty(t, herdr.Code(err))
}

func TestAnErrorEnvelopeOnStandardOutputIsAlsoUnwrapped(t *testing.T) {
	runner := &fakeRunner{replies: map[string]reply{
		"agent list": {out: `{"id":"cli:agent:list","error":{"code":"busy","message":"try later"}}`},
	}}

	_, err := herdr.New(runner).Agents(context.Background())
	require.Error(t, err)
	assert.Equal(t, "busy", herdr.Code(err))
}

func TestAReplyThatIsNotJsonSaysSoRatherThanDecodingToNothing(t *testing.T) {
	runner := &fakeRunner{replies: map[string]reply{"agent list": {out: "not json"}}}

	_, err := herdr.New(runner).Agents(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not read the reply")
}

func TestPromptSendsTheTextAsOneArgumentSoNewlinesSurvive(t *testing.T) {
	text := "line one\nline two"
	runner := &fakeRunner{replies: map[string]reply{
		"agent prompt w1:p3 " + text: {out: `{"result":{}}`},
	}}

	require.NoError(t, herdr.New(runner).Prompt(context.Background(), "w1:p3", text))
	require.Len(t, runner.calls, 1)
	assert.Equal(t, []string{"agent", "prompt", "w1:p3", text}, runner.calls[0])
}

func TestNotifyCarriesABodyOnlyWhenThereIsOne(t *testing.T) {
	runner := &fakeRunner{replies: map[string]reply{
		"notification show title --sound request":             {out: `{"result":{}}`},
		"notification show title --sound request --body body": {out: `{"result":{}}`},
	}}
	client := herdr.New(runner)

	require.NoError(t, client.Notify(context.Background(), "title", ""))
	require.NoError(t, client.Notify(context.Background(), "title", "body"))

	assert.NotContains(t, runner.calls[0], "--body")
	assert.Contains(t, runner.calls[1], "--body")
}

func TestStartingAnAgentPassesTheTimeoutInMilliseconds(t *testing.T) {
	runner := &fakeRunner{replies: map[string]reply{
		"agent start pr-prutil-42 --kind claude --pane w3:p1 --timeout 60000": {
			out: `{"result":{"agent":{"agent":"claude","agent_status":"idle","pane_id":"w3:p1","name":"pr-prutil-42"}}}`,
		},
	}}

	agent, err := herdr.New(runner).StartAgent(context.Background(), "pr-prutil-42", "claude", "w3:p1", time.Minute)
	require.NoError(t, err)
	assert.Equal(t, "pr-prutil-42", agent.Name)
	assert.True(t, agent.Settled())
}

func TestListingWorktreesPassesTheRootAsCwdAndDecodesBranchAndPath(t *testing.T) {
	runner := &fakeRunner{replies: map[string]reply{
		"worktree list --cwd /repos/prutil": {
			out: `{"result":{"worktrees":[{"path":"/repos/prutil","branch":"main","kind":"main"},{"path":"/repos/prutil-pr42","branch":"pr-42"}]}}`,
		},
	}}

	worktrees, err := herdr.New(runner).Worktrees(context.Background(), "/repos/prutil")
	require.NoError(t, err)
	require.Len(t, worktrees, 2)
	assert.Equal(t, "/repos/prutil", worktrees[0].Path)
	assert.Equal(t, "main", worktrees[0].Branch)
	assert.Equal(t, "/repos/prutil-pr42", worktrees[1].Path)
	assert.Equal(t, "pr-42", worktrees[1].Branch)

	require.Len(t, runner.calls, 1)
	assert.Equal(t, []string{"worktree", "list", "--cwd", "/repos/prutil"}, runner.calls[0])
}

func TestCreatingAWorktreePinsTheArgumentOrderAndReadsWorkspaceTabAndRootPane(t *testing.T) {
	runner := &fakeRunner{replies: map[string]reply{
		"worktree create --cwd /repos/prutil --branch pr-42 --label prutil#42 --no-focus": {
			out: `{"result":{"workspace":"ws-1","tab":"tab-9","root_pane":"pane-3"}}`,
		},
	}}

	got, err := herdr.New(runner).CreateWorktree(context.Background(), "/repos/prutil", "pr-42", "prutil#42")
	require.NoError(t, err)
	assert.Equal(t, "ws-1", got.WorkspaceID)
	assert.Equal(t, "tab-9", got.TabID)
	assert.Equal(t, "pane-3", got.RootPaneID)

	require.Len(t, runner.calls, 1)
	assert.Equal(t,
		[]string{"worktree", "create", "--cwd", "/repos/prutil", "--branch", "pr-42", "--label", "prutil#42", "--no-focus"},
		runner.calls[0],
	)
}

func TestOpeningAWorktreeDecodesObjectIdentifiersAndPassesNoFocus(t *testing.T) {
	runner := &fakeRunner{replies: map[string]reply{
		"worktree open --cwd /repos/prutil --path /repos/prutil-pr42 --branch pr-42 --label prutil#42 --no-focus": {
			out: `{"result":{"workspace":{"workspace_id":"ws-2"},"tab":{"id":"tab-4"},"root_pane":{"pane_id":"pane-7"}}}`,
		},
	}}

	got, err := herdr.New(runner).OpenWorktree(context.Background(), "/repos/prutil", "/repos/prutil-pr42", "pr-42", "prutil#42")
	require.NoError(t, err)
	assert.Equal(t, "ws-2", got.WorkspaceID)
	assert.Equal(t, "tab-4", got.TabID)
	assert.Equal(t, "pane-7", got.RootPaneID)

	require.Len(t, runner.calls, 1)
	assert.Equal(t,
		[]string{"worktree", "open", "--cwd", "/repos/prutil", "--path", "/repos/prutil-pr42", "--branch", "pr-42", "--label", "prutil#42", "--no-focus"},
		runner.calls[0],
	)
}

func TestCreatingAWorktreeUnwrapsAServerErrorEnvelopeFromStandardError(t *testing.T) {
	runner := &fakeRunner{replies: map[string]reply{
		"worktree create --cwd /repos/prutil --branch pr-42 --label prutil#42 --no-focus": {
			err: serverError(herdr.CodeAgentNotReady, "server is still loading"),
		},
	}}

	_, err := herdr.New(runner).CreateWorktree(context.Background(), "/repos/prutil", "pr-42", "prutil#42")
	require.Error(t, err)
	assert.Equal(t, herdr.CodeAgentNotReady, herdr.Code(err))
	assert.Contains(t, err.Error(), "server is still loading")
}

func TestOpeningAWorktreeFailsWhenRequiredIdentifiersAreMissing(t *testing.T) {
	runner := &fakeRunner{replies: map[string]reply{
		"worktree open --cwd /repos/prutil --path /repos/prutil-pr42 --branch pr-42 --label prutil#42 --no-focus": {
			out: `{"result":{"workspace":{},"tab":"tab-4","root_pane":"pane-7"}}`,
		},
	}}

	_, err := herdr.New(runner).OpenWorktree(context.Background(), "/repos/prutil", "/repos/prutil-pr42", "pr-42", "prutil#42")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "workspace")
	assert.Contains(t, err.Error(), "could not read the reply")
}

func TestWorktreeCommandsValidateRequiredInputsBeforeAnyProcessCall(t *testing.T) {
	runner := &fakeRunner{}
	client := herdr.New(runner)

	_, err := client.Worktrees(context.Background(), " ")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "worktree list: root is required")

	_, err = client.CreateWorktree(context.Background(), "/repos/prutil", "", "label")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "worktree create: branch is required")

	_, err = client.OpenWorktree(context.Background(), "/repos/prutil", "/path", "pr-42", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "worktree open: label is required")

	assert.Empty(t, runner.calls, "invalid inputs must not spawn a process")
}
