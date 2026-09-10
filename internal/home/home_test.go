package home_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/relloyd/prutil/internal/home"
)

func TestDirPrefersPrutilHomeThenXdgThenTheDefault(t *testing.T) {
	t.Setenv("PRUTIL_HOME", "/tmp/explicit")
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	dir, err := home.Dir()
	require.NoError(t, err)
	assert.Equal(t, "/tmp/explicit", dir)

	t.Setenv("PRUTIL_HOME", "")
	dir, err = home.Dir()
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("/tmp/xdg", "prutil"), dir)

	t.Setenv("XDG_CONFIG_HOME", "")
	dir, err = home.Dir()
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(dir, filepath.Join(".config", "prutil")), dir)
}

func TestAMissingConfigurationIsTheDefaultsRatherThanAnError(t *testing.T) {
	store := home.OpenIn(t.TempDir())

	cfg, err := store.LoadConfig()
	require.NoError(t, err)
	assert.Equal(t, home.DefaultConfig(), cfg)
	_, err = os.Stat(store.Path(home.ConfigFile))
	assert.ErrorIs(t, err, os.ErrNotExist, "LoadConfig stays read-only and does not create config.yaml")
}

func TestAConfigurationSettingOneKeyLeavesEveryOtherAtItsDefault(t *testing.T) {
	cfg, err := home.ParseConfig([]byte("herdr:\n  skill: pr-triage\n"))
	require.NoError(t, err)

	assert.Equal(t, "pr-triage", cfg.Herdr.Skill)
	assert.Equal(t, home.DefaultPrompt, cfg.Herdr.Prompt, "the prompt keeps its default")
	assert.True(t, cfg.Herdr.Toast, "toast stays on rather than dropping to the zero value")
	assert.Equal(t, 2*time.Minute, cfg.Watch.BaseInterval.Duration())
}

func TestDurationsReadTheWayTheyAreWritten(t *testing.T) {
	cfg, err := home.ParseConfig([]byte("watch:\n  base_interval: 45s\n  max_interval: 1h30m\n"))
	require.NoError(t, err)

	assert.Equal(t, 45*time.Second, cfg.Watch.BaseInterval.Duration())
	assert.Equal(t, 90*time.Minute, cfg.Watch.MaxInterval.Duration())
}

func TestADurationThatIsNotOneIsReportedRatherThanIgnored(t *testing.T) {
	_, err := home.ParseConfig([]byte("watch:\n  base_interval: soon\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "soon")
}

func TestPollIntervalsAreClampedSoATypoCannotHammerGitHub(t *testing.T) {
	cfg, err := home.ParseConfig([]byte("watch:\n  base_interval: 1s\n  max_interval: 2s\n"))
	require.NoError(t, err)

	assert.Equal(t, 15*time.Second, cfg.Watch.BaseInterval.Duration())
	assert.Equal(t, 15*time.Second, cfg.Watch.MaxInterval.Duration())
}

func TestACapBelowTheIntervalItCapsIsRaisedToIt(t *testing.T) {
	cfg, err := home.ParseConfig([]byte("watch:\n  base_interval: 20m\n  max_interval: 30s\n"))
	require.NoError(t, err)

	assert.Equal(t, 20*time.Minute, cfg.Watch.MaxInterval.Duration(),
		"a backoff must never run backwards")
}

func TestTheDefaultPromptInvokesTheConfiguredSkill(t *testing.T) {
	cfg := home.DefaultConfig()
	cfg.Herdr.Skill = "pr-triage"

	text, err := cfg.Herdr.RenderPrompt(home.PromptData{
		Repo: "relloyd/prutil", Number: 42,
		URL: "https://github.com/relloyd/prutil/pull/42",
	})
	require.NoError(t, err)
	assert.Equal(t, "/pr-triage https://github.com/relloyd/prutil/pull/42", text)
}

func TestTheDefaultPromptSpellsTheJobOutWhenNoSkillIsConfigured(t *testing.T) {
	cfg := home.DefaultConfig()

	text, err := cfg.Herdr.RenderPrompt(home.PromptData{
		Repo: "relloyd/prutil", Number: 42,
		URL:     "https://github.com/relloyd/prutil/pull/42",
		HeadRef: "feat/retry", BaseRef: "main",
		UnresolvedCount: 1,
	})
	require.NoError(t, err)
	assert.Contains(t, text, "relloyd/prutil#42")
	assert.Contains(t, text, "feat/retry into main")
	assert.Contains(t, text, "1 unresolved review thread.", "one thread is singular")
}

func TestAPromptNoteIsAppendedSoTheAgentKnowsAboutTheWrongBranch(t *testing.T) {
	cfg := home.DefaultConfig()
	cfg.Herdr.Skill = "pr-triage"

	text, err := cfg.Herdr.RenderPrompt(home.PromptData{
		URL:  "https://example.test/pull/1",
		Note: "The checkout is on main.",
	})
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(text, "The checkout is on main."), text)
}

func TestAPromptTemplateThatDoesNotParseIsReported(t *testing.T) {
	cfg := home.DefaultConfig()
	cfg.Herdr.Prompt = "{{.URL"

	_, err := cfg.Herdr.RenderPrompt(home.PromptData{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a valid template")
}

func TestTheWatchStateSurvivesARoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := home.OpenIn(dir)

	state := home.NewState()
	state.SetArmed("relloyd/prutil#42", true)
	state.RecordHandoff("relloyd/prutil#42", map[string]string{"T1": "C1"}, time.Unix(1_700_000_000, 0).UTC())
	require.NoError(t, store.SaveState(state))

	read, err := home.OpenIn(dir).LoadState()
	require.NoError(t, err)
	assert.True(t, read.Armed("relloyd/prutil#42"))
	assert.Equal(t, map[string]string{"T1": "C1"}, read.Get("relloyd/prutil#42").NotifiedThreads)
}

func TestAMissingStateFileIsAnEmptyState(t *testing.T) {
	state, err := home.OpenIn(t.TempDir()).LoadState()
	require.NoError(t, err)
	assert.Equal(t, 0, state.ArmedCount())
}

func TestTogglingArmsThenDisarms(t *testing.T) {
	state := home.NewState()
	assert.True(t, state.ToggleArmed("a/b#1"))
	assert.Equal(t, []string{"a/b#1"}, state.ArmedKeys())
	assert.False(t, state.ToggleArmed("a/b#1"))
	assert.Empty(t, state.ArmedKeys())
}

func TestCompactDropsEntriesThatRememberNothingWorthKeeping(t *testing.T) {
	state := home.NewState()
	state.SetArmed("a/b#1", true)
	state.ToggleArmed("a/b#2")
	state.ToggleArmed("a/b#2")
	state.RecordHandoff("a/b#3", map[string]string{"T1": "C1"}, time.Now())

	state.Compact()

	assert.Contains(t, state.PRs, "a/b#1", "an armed pull request stays")
	assert.NotContains(t, state.PRs, "a/b#2", "one armed then disarmed goes")
	assert.Contains(t, state.PRs, "a/b#3", "one with threads handed off stays, so they are not sent twice")
}

func TestSavingReplacesTheFileRatherThanLeavingATemporaryOneBehind(t *testing.T) {
	dir := t.TempDir()
	store := home.OpenIn(dir)

	state := home.NewState()
	state.SetArmed("a/b#1", true)
	require.NoError(t, store.SaveState(state))
	require.NoError(t, store.SaveState(state))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	assert.Equal(t, []string{home.StateFile}, names)
}

func TestEveryHandoffIsAppendedAsItsOwnLine(t *testing.T) {
	dir := t.TempDir()
	store := home.OpenIn(dir)

	require.NoError(t, store.AppendHandoff(home.Handoff{PR: "a/b#1", Outcome: home.OutcomeSent}))
	require.NoError(t, store.AppendHandoff(home.Handoff{PR: "a/b#2", Outcome: home.OutcomeNoAgent}))

	data, err := os.ReadFile(filepath.Join(dir, home.HandoffFile))
	require.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, 2)
	for _, line := range lines {
		var got home.Handoff
		require.NoError(t, json.Unmarshal([]byte(line), &got))
		assert.NotEmpty(t, got.PR)
	}
}

func TestRecentHandoffsReturnsOnlyTheSelectedPullRequestNewestFirst(t *testing.T) {
	store := home.OpenIn(t.TempDir())
	require.NoError(t, store.AppendHandoff(home.Handoff{PR: "a/b#1", Outcome: home.OutcomeSent}))
	require.NoError(t, store.AppendHandoff(home.Handoff{PR: "a/b#2", Outcome: home.OutcomeNoAgent}))
	require.NoError(t, store.AppendHandoff(home.Handoff{PR: "a/b#1", Outcome: home.OutcomeFailed}))

	history, err := store.RecentHandoffs("a/b#1", 1)

	require.NoError(t, err)
	require.Len(t, history, 1)
	assert.Equal(t, home.OutcomeFailed, history[0].Outcome)
}

func TestRecentHandoffsReportsMalformedHistoryRatherThanIgnoringIt(t *testing.T) {
	dir := t.TempDir()
	store := home.OpenIn(dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, home.HandoffFile), []byte("{not json}\n"), 0o600))

	_, err := store.RecentHandoffs("a/b#1", 3)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "line 1")
}
