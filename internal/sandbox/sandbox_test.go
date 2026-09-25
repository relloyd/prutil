package sandbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/relloyd/prutil/internal/run"
	"github.com/relloyd/prutil/internal/sandbox"
)

// fakeClaude answers claude sandbox status the way Claude Code 2.1.282 does,
// and records where and how it was asked.
type fakeClaude struct {
	mu    sync.Mutex
	calls []call
	reply string
	err   error
}

type call struct {
	dir  string
	args []string
}

func (f *fakeClaude) RunIn(_ context.Context, dir string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call{dir: dir, args: args})
	return []byte(f.reply), f.err
}

const (
	statusStrict = `{"statusVersion":3,"available":false,"installed":false,"policyLocked":false,` +
		`"reasons":["Windows sandbox install is only available on native Windows."],"supported":true,` +
		`"enabled":true,"enabledSource":"policy","unavailableReason":null,"strictMode":true,` +
		`"strictModeSource":"policy","filesystemPolicy":"strict","autoAllowBashIfSandboxed":true,` +
		`"autoAllowBashIfSandboxedSource":"policy"}`
	statusOff = `{"statusVersion":3,"supported":true,"enabled":false,"enabledSource":"off",` +
		`"unavailableReason":null,"strictMode":false}`
	statusUnsupported = `{"statusVersion":3,"supported":false,"enabled":false,"enabledSource":"off",` +
		`"unavailableReason":"bubblewrap is not installed","strictMode":false}`
)

// newSandbox builds a registry over a temporary prutil home, with the SSH
// socket behind a symbolic link the way macOS has it.
func newSandbox(t *testing.T, cli *fakeClaude) (*sandbox.Sandbox, sandbox.Env) {
	t.Helper()
	root := t.TempDir()
	env := sandbox.Env{
		Dir:         filepath.Join(root, "home", ".config", "prutil", "sandbox"),
		Home:        filepath.Join(root, "home"),
		SSHAuthSock: "/var/run/com.apple.launchd.abc/Listeners",
		RealPath: func(p string) (string, error) {
			return strings.Replace(p, "/var/", "/private/var/", 1), nil
		},
	}
	s := sandbox.New(sandbox.Options{
		Env:  env,
		Look: func(bin string) (sandbox.Runner, error) { return cli, nil },
	})
	return s, env
}

func target(dir string) sandbox.Target {
	return sandbox.Target{Dir: dir, Host: "github.com", Owner: "relloyd"}
}

// launchSettings reads back the file a launch pointed claude at.
func launchSettings(t *testing.T, l sandbox.Launch) map[string]any {
	t.Helper()
	require.Len(t, l.Args, 2)
	require.Equal(t, "--settings", l.Args[0])
	data, err := os.ReadFile(l.Args[1])
	require.NoError(t, err)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(data, &doc))
	return doc
}

func at(doc map[string]any, path ...string) any {
	var v any = doc
	for _, key := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[key]
	}
	return v
}

func TestOnlyClaudeCanBeStartedContainedSoFar(t *testing.T) {
	s, _ := newSandbox(t, &fakeClaude{reply: statusStrict})

	assert.True(t, s.Supports("claude"))
	assert.False(t, s.Supports("copilot"), "an entry with nothing filled in yet")
	assert.False(t, s.Supports("agy"))
	assert.False(t, s.Supports("codex"), "no entry at all")
	assert.False(t, (*sandbox.Sandbox)(nil).Supports("claude"), "no registry means nothing is contained")

	_, err := s.Launch(context.Background(), "copilot", target(t.TempDir()))
	require.ErrorIs(t, err, sandbox.ErrUnsupported)
}

func TestLaunchingAClaudeAgentVerifiesItsPolicyFromWhereItWillRun(t *testing.T) {
	cli := &fakeClaude{reply: statusStrict}
	s, _ := newSandbox(t, cli)
	dir := t.TempDir()

	l, err := s.Launch(context.Background(), "claude", target(dir))
	require.NoError(t, err)

	assert.Equal(t, sandbox.Posture{Known: true, Contained: true, Strict: true,
		Detail: "claude sandbox on, from policy, strict"}, l.Posture)
	require.Len(t, cli.calls, 1)
	assert.Equal(t, dir, cli.calls[0].dir,
		"the project's own settings are part of what an agent gets, so the question is asked there")
	assert.Equal(t, []string{"--settings", l.Args[1], "sandbox", "status"}, cli.calls[0].args,
		"Claude is asked about the very file the agent will be given")
}

func TestThePolicyShipsWithTheProtectionsTheLiveProbesProved(t *testing.T) {
	s, env := newSandbox(t, &fakeClaude{reply: statusStrict})

	_, err := s.Launch(context.Background(), "claude", target(t.TempDir()))
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(env.Dir, "claude-settings.json"))
	require.NoError(t, err)
	var policy map[string]any
	require.NoError(t, json.Unmarshal(data, &policy))

	assert.Equal(t, true, at(policy, "sandbox", "enabled"))
	assert.Equal(t, false, at(policy, "sandbox", "allowUnsandboxedCommands"),
		"strict: the model cannot ask its way out")
	assert.Equal(t, true, at(policy, "sandbox", "autoAllowBashIfSandboxed"),
		"an unattended agent cannot stop at a dialog")
	assert.Nil(t, at(policy, "sandbox", "network", "allowUnixSockets"),
		"the SSH socket moves every boot, so it belongs in the launch file, not the reader's")
	assert.ElementsMatch(t, []any{"com.apple.SecurityServer", "com.apple.trustd.agent", "com.apple.trustd"},
		at(policy, "sandbox", "network", "allowMachLookup"), "gh needs the keychain and Go needs trustd")

	deny, _ := at(policy, "permissions", "deny").([]any)
	for _, rule := range []string{
		"Read(~/.ssh/id_*)", "Read(~/.aws/**)", "Read(~/.gnupg/**)", "Read(~/.netrc)",
		"Read(~/.config/prutil/**)", "Read(~/.config/herdr/**)",
		"Edit(.github/workflows/**)", "Edit(.claude/**)", "Edit(.mcp.json)",
	} {
		assert.Contains(t, deny, rule)
	}
	for _, rule := range deny {
		assert.NotContains(t, rule, "Bash(", "a Bash rule matches only what is typed, so a script walks past it")
		assert.NotContains(t, rule, ".config/gh", "gh cannot reply on a thread without its configuration")
	}
}

func TestThePolicyIsWrittenOnceAndTheReadersEditsSurvive(t *testing.T) {
	cli := &fakeClaude{reply: statusStrict}
	s, env := newSandbox(t, cli)
	_, err := s.Launch(context.Background(), "claude", target(t.TempDir()))
	require.NoError(t, err)

	policyPath := filepath.Join(env.Dir, "claude-settings.json")
	info, err := os.Stat(policyPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "a policy is nobody else's business")

	edited := `{"sandbox":{"enabled":true,"allowUnsandboxedCommands":false,` +
		`"network":{"allowedDomains":["github.com","registry.npmjs.org"]}}}`
	require.NoError(t, os.WriteFile(policyPath, []byte(edited), 0o600))

	l, err := s.Launch(context.Background(), "claude", target(t.TempDir()))
	require.NoError(t, err)

	got, err := os.ReadFile(policyPath)
	require.NoError(t, err)
	assert.JSONEq(t, edited, string(got), "prutil never writes the reader's file again")
	assert.Contains(t, at(launchSettings(t, l), "sandbox", "network", "allowedDomains"), "registry.npmjs.org",
		"and the agent gets what the reader wrote")
}

func TestTheLaunchAddsTheSSHSocketUnderBothItsNames(t *testing.T) {
	s, _ := newSandbox(t, &fakeClaude{reply: statusStrict})

	l, err := s.Launch(context.Background(), "claude", target(t.TempDir()))
	require.NoError(t, err)

	assert.Equal(t, []any{"/var/run/com.apple.launchd.abc/Listeners", "/private/var/run/com.apple.launchd.abc/Listeners"},
		at(launchSettings(t, l), "sandbox", "network", "allowUnixSockets"),
		"the sandbox matches a socket's real path, and /var is a link to /private/var")
}

func TestAnEnterpriseHostIsAddedToTheNetworkAllowlist(t *testing.T) {
	s, _ := newSandbox(t, &fakeClaude{reply: statusStrict})

	l, err := s.Launch(context.Background(), "claude",
		sandbox.Target{Dir: t.TempDir(), Host: "github.acme.example", Owner: "widgets"})
	require.NoError(t, err)

	assert.Contains(t, at(launchSettings(t, l), "sandbox", "network", "allowedDomains"), "github.acme.example")
}

func TestLaunchRefusesANameThatCouldNotBeAGitHubOwner(t *testing.T) {
	s, _ := newSandbox(t, &fakeClaude{reply: statusStrict})

	_, err := s.Launch(context.Background(), "claude",
		sandbox.Target{Dir: t.TempDir(), Host: "github.com", Owner: "a b/c"})
	require.Error(t, err, "it becomes part of a git config key")
}

func TestTheReadersOwnGitConfigEntriesComeFirst(t *testing.T) {
	s, env := newSandbox(t, &fakeClaude{reply: statusStrict})
	require.NoError(t, os.MkdirAll(env.Dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(env.Dir, "claude-settings.json"), []byte(
		`{"sandbox":{"enabled":true},"env":{"GIT_CONFIG_COUNT":"1",`+
			`"GIT_CONFIG_KEY_0":"core.pager","GIT_CONFIG_VALUE_0":"cat"}}`), 0o600))

	l, err := s.Launch(context.Background(), "claude", target(t.TempDir()))
	require.NoError(t, err)
	envBlock, _ := at(launchSettings(t, l), "env").(map[string]any)

	assert.Equal(t, "core.pager", envBlock["GIT_CONFIG_KEY_0"], "the reader's entry keeps its place")
	assert.Equal(t, "url.https://github.com/relloyd/.insteadOf", envBlock["GIT_CONFIG_KEY_1"])
	assert.Equal(t, "13", envBlock["GIT_CONFIG_COUNT"], "one of theirs and twelve of prutil's")
}

func TestLaunchFilesAreNamedByContentAndNeverRewritten(t *testing.T) {
	s, _ := newSandbox(t, &fakeClaude{reply: statusStrict})

	a, err := s.Launch(context.Background(), "claude", target(t.TempDir()))
	require.NoError(t, err)
	again, err := s.Launch(context.Background(), "claude", target(t.TempDir()))
	require.NoError(t, err)
	other, err := s.Launch(context.Background(), "claude",
		sandbox.Target{Dir: t.TempDir(), Host: "github.com", Owner: "someone-else"})
	require.NoError(t, err)

	assert.Equal(t, a.Args[1], again.Args[1], "the same policy is the same file")
	assert.NotEqual(t, a.Args[1], other.Args[1],
		"a different owner is a different file, so a running agent's file never changes under it")
}

func TestAPolicyClaudeDoesNotSandboxIsReportedAsSuch(t *testing.T) {
	cases := []struct {
		name  string
		reply string
		want  sandbox.Posture
	}{
		{
			name:  "the reader turned it off",
			reply: statusOff,
			want:  sandbox.Posture{Known: true, Detail: "claude sandbox off"},
		},
		{
			name:  "the platform cannot sandbox at all",
			reply: statusUnsupported,
			want:  sandbox.Posture{Known: true, Detail: "claude sandbox not supported here: bubblewrap is not installed"},
		},
		{
			name:  "a warning printed before the status line is skipped",
			reply: "warning: something unrelated\n" + statusStrict + "\n",
			want:  sandbox.Posture{Known: true, Contained: true, Strict: true, Detail: "claude sandbox on, from policy, strict"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newSandbox(t, &fakeClaude{reply: tc.reply})
			l, err := s.Launch(context.Background(), "claude", target(t.TempDir()))
			require.NoError(t, err)
			assert.Equal(t, tc.want, l.Posture)
		})
	}
}

func TestAStatusPrutilCannotReadIsAnErrorRatherThanAGuess(t *testing.T) {
	s, _ := newSandbox(t, &fakeClaude{reply: "error: unknown command 'sandbox'"})

	_, err := s.Launch(context.Background(), "claude", target(t.TempDir()))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown command", "and says what claude said")
}

func TestInspectAsksWithTheSettingsTheAgentWasLaunchedWith(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want []string
	}{
		{
			name: "a settings file given as its own argument",
			argv: []string{"claude", "--settings", "/p/launch/claude-1.json"},
			want: []string{"--settings", "/p/launch/claude-1.json", "sandbox", "status"},
		},
		{
			name: "a settings file given with an equals sign",
			argv: []string{"claude", "--model", "x", "--settings=/p/launch/claude-1.json"},
			want: []string{"--settings", "/p/launch/claude-1.json", "sandbox", "status"},
		},
		{
			name: "an agent the reader started with no settings of its own",
			argv: []string{"claude"},
			want: []string{"sandbox", "status"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cli := &fakeClaude{reply: statusStrict}
			s, _ := newSandbox(t, cli)

			_, err := s.Inspect(context.Background(), "claude", "/work/wt", tc.argv)
			require.NoError(t, err)
			require.Len(t, cli.calls, 1)
			assert.Equal(t, "/work/wt", cli.calls[0].dir)
			assert.Equal(t, tc.want, cli.calls[0].args)
		})
	}
}

func TestInspectRemembersAnAnswerBrieflyAndAFailureNotAtAll(t *testing.T) {
	cli := &fakeClaude{reply: statusStrict}
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	s := sandbox.New(sandbox.Options{
		Env:  sandbox.Env{Dir: t.TempDir()},
		Look: func(string) (sandbox.Runner, error) { return cli, nil },
		Now:  func() time.Time { return now },
	})
	argv := []string{"claude"}

	_, err := s.Inspect(context.Background(), "claude", "/wt", argv)
	require.NoError(t, err)
	_, err = s.Inspect(context.Background(), "claude", "/wt", argv)
	require.NoError(t, err)
	assert.Len(t, cli.calls, 1, "one process per agent per minute, not per handoff")

	now = now.Add(2 * time.Minute)
	cli.err, cli.reply = errors.New("claude crashed"), ""
	_, err = s.Inspect(context.Background(), "claude", "/wt", argv)
	require.Error(t, err)
	_, err = s.Inspect(context.Background(), "claude", "/wt", argv)
	require.Error(t, err)
	assert.Len(t, cli.calls, 3, "a failure is asked about again rather than remembered")
}

func TestAKindThatCannotBeAskedIsNeverCountedAsContained(t *testing.T) {
	s, _ := newSandbox(t, &fakeClaude{reply: statusStrict})

	p, err := s.Inspect(context.Background(), "copilot", "/wt", []string{"copilot", "--sandbox"})
	require.NoError(t, err)
	assert.False(t, p.Known)
	assert.False(t, p.Contained)
	assert.Equal(t, "sandbox unknown", p.String())
}

func TestPostureNamesItselfForTheLog(t *testing.T) {
	assert.Equal(t, "sandboxed, strict", sandbox.Posture{Known: true, Contained: true, Strict: true}.String())
	assert.Equal(t, "sandboxed", sandbox.Posture{Known: true, Contained: true}.String())
	assert.Equal(t, "not sandboxed", sandbox.Posture{Known: true}.String())
	assert.Equal(t, "sandbox unknown", sandbox.Posture{}.String())
}

// TestTheGitRulesWinAgainstAReadersSSHRewrite runs real git, but only
// git remote get-url, which reads configuration and never the network. The
// behaviour being checked is git's own prefix matching, which no fake could
// stand in for. A reader's work-style global configuration is simulated in a
// temporary file, so theirs is never read.
func TestTheGitRulesWinAgainstAReadersSSHRewrite(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not on PATH")
	}
	s, _ := newSandbox(t, &fakeClaude{reply: statusStrict})
	l, err := s.Launch(context.Background(), "claude", target(t.TempDir()))
	require.NoError(t, err)
	envBlock, _ := at(launchSettings(t, l), "env").(map[string]any)

	dir := t.TempDir()
	global := filepath.Join(dir, "gitconfig")
	require.NoError(t, os.WriteFile(global, []byte(
		"[url \"git@github.com:\"]\n\tinsteadOf = https://github.com/\n"+
			"[url \"ssh://git@github.com/\"]\n\tpushInsteadOf = https://github.com/\n"), 0o600))

	gitEnv := []string{"GIT_CONFIG_GLOBAL=" + global, "GIT_CONFIG_NOSYSTEM=1", "HOME=" + dir, "PATH=" + os.Getenv("PATH")}
	for k, v := range envBlock {
		gitEnv = append(gitEnv, k+"="+v.(string))
	}
	git := func(args ...string) string {
		cmd := exec.Command(gitBin, args...)
		cmd.Dir, cmd.Env = dir, gitEnv
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("remote", "add", "https-origin", "https://github.com/relloyd/prutil.git")
	git("remote", "add", "scp-origin", "git@github.com:relloyd/prutil.git")
	git("remote", "add", "ssh-origin", "ssh://git@github.com/relloyd/prutil.git")

	for _, remote := range []string{"https-origin", "scp-origin", "ssh-origin"} {
		assert.Equal(t, "https://github.com/relloyd/prutil.git", git("remote", "get-url", remote),
			"%s fetches over HTTPS", remote)
		assert.Equal(t, "https://github.com/relloyd/prutil.git", git("remote", "get-url", "--push", remote),
			"%s pushes over HTTPS, pushInsteadOf and all", remote)
	}
	assert.Equal(t, "https://github.com/someone/else.git", git("ls-remote", "--get-url", "git@github.com:someone/else.git"),
		"another owner's SSH remote goes over HTTPS too")
	assert.Equal(t, "git@github.com:someone/else.git", git("ls-remote", "--get-url", "https://github.com/someone/else.git"),
		"but the reader's own rule still wins for another owner's HTTPS remote")
}

// TestTheRealClaudeAcceptsThePolicyAsStrict asks the installed claude binary
// about a generated policy. It starts no agent and contacts no model; set
// PRUTIL_LIVE_CLAUDE=1 to run it.
func TestTheRealClaudeAcceptsThePolicyAsStrict(t *testing.T) {
	if os.Getenv("PRUTIL_LIVE_CLAUDE") != "1" {
		t.Skip("set PRUTIL_LIVE_CLAUDE=1 to ask the installed claude")
	}
	cmd, err := run.Look("claude", "")
	require.NoError(t, err)
	root := t.TempDir()
	s := sandbox.New(sandbox.Options{
		Env: sandbox.Env{
			Dir:         filepath.Join(root, "sandbox"),
			Home:        root,
			SSHAuthSock: os.Getenv("SSH_AUTH_SOCK"),
		},
		Look: func(string) (sandbox.Runner, error) { return cmd, nil },
	})

	l, err := s.Launch(context.Background(), "claude", target(t.TempDir()))
	require.NoError(t, err)
	assert.True(t, l.Posture.Contained, l.Posture.Detail)
	assert.True(t, l.Posture.Strict, l.Posture.Detail)
}
