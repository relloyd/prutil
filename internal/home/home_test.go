package home_test

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/relloyd/prutil/internal/home"
	"github.com/relloyd/prutil/internal/model"
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

func TestTheDefaultCheckPromptExplainsHowToTriageAndRetryFailures(t *testing.T) {
	cfg := home.DefaultConfig()

	text, err := cfg.Herdr.RenderCheckPrompt(home.PromptData{
		Repo: "relloyd/prutil", Number: 42, URL: "https://github.com/relloyd/prutil/pull/42",
		HeadRef: "feat/retry", BaseRef: "main",
		Checks: []model.Check{{Name: "linux", Workflow: "CI", URL: "https://example.test/check", Description: "failed"}},
	})
	require.NoError(t, err)
	assert.Contains(t, text, "related to these changes")
	assert.Contains(t, text, "follow-up commit")
	assert.Contains(t, text, "re-triggered that check")
	assert.Contains(t, text, "ask the human")
	assert.Contains(t, text, "linux (CI): failed https://example.test/check")
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

func TestDisarmingClearsTheLastFailedCheckHandoffHead(t *testing.T) {
	state := home.NewState()
	state.SetArmed("a/b#1", true)
	state.Mutate("a/b#1").LastCheckHandoffHead = "abc"

	state.ToggleArmed("a/b#1")

	assert.Empty(t, state.Get("a/b#1").LastCheckHandoffHead)
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

func TestRecentHandoffsReadsPastALineItCannotParse(t *testing.T) {
	// The log is appended to a line at a time by one writer, so the only line
	// that can be damaged is the last, and the damage is a write cut short.
	// Failing the whole read over it would lose every handoff before it too,
	// for good, on every pull request at once.
	dir := t.TempDir()
	store := home.OpenIn(dir)
	good := home.Handoff{At: time.Now(), PR: "a/b#1", Outcome: home.OutcomeSent}
	encoded, err := json.Marshal(good)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, home.HandoffFile),
		append(append(encoded, '\n'), []byte(`{"pr":"a/b#1","outc`)...), 0o600))

	history, err := store.RecentHandoffs("a/b#1", 3)

	require.NoError(t, err)
	require.Len(t, history, 1, "the readable handoff is still readable")
	assert.Equal(t, home.OutcomeSent, history[0].Outcome)
}

func TestTheHandoffLogIsRolledOnceItGrowsPastItsLimit(t *testing.T) {
	dir := t.TempDir()
	store := home.OpenIn(dir)

	// Fill the log past its limit with entries for one pull request.
	for i := 0; i < 3000; i++ {
		require.NoError(t, store.AppendHandoff(home.Handoff{
			At:      time.Now().Add(time.Duration(i) * time.Second),
			PR:      "a/b#1",
			Outcome: home.OutcomeSent,
			Prompt:  strings.Repeat("x", 512),
		}))
	}
	require.FileExists(t, filepath.Join(dir, home.PreviousHandoffFile), "the log was rolled")

	info, err := os.Stat(filepath.Join(dir, home.HandoffFile))
	require.NoError(t, err)
	assert.Less(t, info.Size(), int64(1<<20), "and the live log started again")

	// A roll must not blank the pane: reading sees through it.
	history, err := store.RecentHandoffs("a/b#1", 3)
	require.NoError(t, err)
	assert.Len(t, history, 3, "the newest handoffs are still readable across the roll")
}

func TestReadingSeesHandoffsLeftInTheRolledLog(t *testing.T) {
	dir := t.TempDir()
	store := home.OpenIn(dir)
	older := home.Handoff{At: time.Now().Add(-time.Hour), PR: "a/b#1", Outcome: home.OutcomeNoAgent}
	encoded, err := json.Marshal(older)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, home.PreviousHandoffFile), append(encoded, '\n'), 0o600))
	require.NoError(t, store.AppendHandoff(home.Handoff{At: time.Now(), PR: "a/b#1", Outcome: home.OutcomeSent}))

	history, err := store.RecentHandoffs("a/b#1", 5)

	require.NoError(t, err)
	require.Len(t, history, 2)
	assert.Equal(t, home.OutcomeSent, history[0].Outcome, "newest first")
	assert.Equal(t, home.OutcomeNoAgent, history[1].Outcome, "then what the roll left behind")
}

func TestLoadCreatesTheDirectoryAndAConfigurationOnFirstRun(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "prutil")
	require.NoDirExists(t, dir)

	loaded := home.OpenIn(dir).Load()

	assert.Empty(t, loaded.Notes, "a first run is not a failure")
	assert.FileExists(t, filepath.Join(dir, home.ConfigFile), "a template to edit beats a page of documentation")
	assert.Equal(t, home.DefaultConfig().Watch.BaseInterval, loaded.Config.Watch.BaseInterval)
	assert.NotNil(t, loaded.State)
	assert.Zero(t, loaded.State.ArmedCount())

	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.Equal(t, fs.FileMode(0o700), info.Mode().Perm(), "the directory names local paths, so it stays private")
}

func TestLoadKeepsWorkingWhenTheConfigurationWillNotParse(t *testing.T) {
	// A typo in a duration is a character somebody mistyped, not a reason to
	// take watching away for the rest of the session.
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, home.ConfigFile),
		[]byte("watch:\n  base_interval: [not, a, duration]\n"), 0o600))
	store := home.OpenIn(dir)

	loaded := store.Load()

	require.Len(t, loaded.Notes, 1)
	assert.Contains(t, loaded.Notes[0].Error(), "is not a duration")
	assert.Contains(t, loaded.Notes[0].Error(), "the built-in defaults are in use")
	assert.Equal(t, home.DefaultConfig().Watch.BaseInterval, loaded.Config.Watch.BaseInterval)

	unchanged, err := os.ReadFile(filepath.Join(dir, home.ConfigFile))
	require.NoError(t, err)
	assert.Contains(t, string(unchanged), "not, a, duration",
		"the reader wrote this file on purpose, so prutil neither moves nor rewrites it")
	assert.NoFileExists(t, filepath.Join(dir, home.ConfigFile+home.CorruptSuffix))
}

func TestLoadMovesAnUnreadableWatchStateAsideBeforeStartingEmpty(t *testing.T) {
	// Starting empty means the next arming overwrites the file, so a half
	// written one is moved out from under that rather than left beneath it.
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, home.StateFile),
		[]byte(`{"prs":{"acme/widgets#1":{"arme`), 0o600))
	store := home.OpenIn(dir)

	loaded := store.Load()

	require.Len(t, loaded.Notes, 1)
	assert.Contains(t, loaded.Notes[0].Error(), "watching starts from nothing")
	assert.Zero(t, loaded.State.ArmedCount())

	kept, err := os.ReadFile(filepath.Join(dir, home.StateFile+home.CorruptSuffix))
	require.NoError(t, err)
	assert.Contains(t, string(kept), "acme/widgets#1", "what was readable is still there to read")
	assert.NoFileExists(t, filepath.Join(dir, home.StateFile), "the unreadable file is out of the way")

	// The store is fully usable, which is the whole point.
	loaded.State.SetArmed("acme/widgets#2", true)
	require.NoError(t, store.SaveState(loaded.State))
	reread, err := store.LoadState()
	require.NoError(t, err)
	assert.True(t, reread.Armed("acme/widgets#2"))
}

func TestLoadReportsBothFilesWhenBothAreUnreadable(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, home.ConfigFile), []byte("watch: [nope]\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, home.StateFile), []byte("{{{"), 0o600))

	loaded := home.OpenIn(dir).Load()

	assert.Len(t, loaded.Notes, 2, "each file gets its own explanation")
	assert.NotNil(t, loaded.State)
	assert.Equal(t, home.DefaultPrompt, loaded.Config.Herdr.Prompt)
}

func TestAWatchStateThatCannotBeOpenedIsNotMovedAside(t *testing.T) {
	// Moving a file aside is for one prutil could read but not understand. One
	// it could not open at all it has no business touching, and most likely no
	// permission to touch either.
	dir := t.TempDir()
	path := filepath.Join(dir, home.StateFile)
	require.NoError(t, os.MkdirAll(path, 0o700))

	loaded := home.OpenIn(dir).Load()

	require.Len(t, loaded.Notes, 1)
	assert.NotErrorIs(t, loaded.Notes[0], home.ErrUnreadable)
	assert.DirExists(t, path, "prutil left it exactly where it found it")
	assert.NoFileExists(t, path+home.CorruptSuffix)
	assert.NoDirExists(t, path+home.CorruptSuffix)
	assert.Zero(t, loaded.State.ArmedCount(), "and still started, with nothing armed")
}
