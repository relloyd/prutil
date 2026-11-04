package sandbox_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeVendorPolicy(t *testing.T, path string, doc any) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	data, err := json.Marshal(doc)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, data, 0o600))
}

func copilotPolicy(home string) map[string]any {
	paths := []string{}
	for _, name := range []string{".ssh", ".aws", ".gnupg", ".netrc", ".config/herdr", ".config/prutil"} {
		paths = append(paths, filepath.Join(home, name))
	}
	return map[string]any{"sandbox": map[string]any{
		"allowBypass": false,
		"userPolicy": map[string]any{
			"filesystem": map[string]any{"deniedPaths": paths},
			"network":    map[string]any{"allowLocalNetwork": false},
		},
	}}
}

func agyPolicy() map[string]any {
	return map[string]any{
		"enableTerminalSandbox": true,
		"toolPermission":        "proceed-in-sandbox",
		"permissions":           map[string]any{"allow": []string{"read_url(github.com)", "read_url(api.github.com)"}},
	}
}

func TestCopilotLaunchNeedsTheReadersStrictPolicy(t *testing.T) {
	t.Setenv("COPILOT_HOME", "")
	s, env := newSandbox(t, &fakeClaude{})
	path := filepath.Join(env.Home, ".copilot", "settings.json")
	l, err := s.Launch(context.Background(), "copilot", target(t.TempDir()))
	require.NoError(t, err)
	assert.False(t, l.Posture.Contained, "a missing policy does not pass the automatic gate")

	writeVendorPolicy(t, path, copilotPolicy(env.Home))
	l, err = s.Launch(context.Background(), "copilot", target(t.TempDir()))
	require.NoError(t, err)
	assert.Equal(t, []string{"--experimental", "--sandbox",
		"--secret-env-vars=AWS_ACCESS_KEY_ID,AWS_SECRET_ACCESS_KEY,AWS_SESSION_TOKEN"}, l.Args)
	assert.True(t, l.Posture.Contained)
	assert.True(t, l.Posture.Strict)
}

func TestCopilotInspectRejectsMissingFlagsAndPermissivePolicies(t *testing.T) {
	t.Setenv("COPILOT_HOME", "")
	for _, tc := range []struct {
		name   string
		argv   []string
		change func(map[string]any)
	}{
		{"a command line without a sandbox switch", []string{"copilot"}, nil},
		{"a prompt mentioning a sandbox switch", []string{"copilot", "-p", "--sandbox"}, nil},
		{"a sandbox switch without experimental features", []string{"copilot", "--sandbox"}, nil},
		{"a command line disabling experimental features", []string{"copilot", "--sandbox", "--no-experimental"}, nil},
		{"a policy that permits bypass", []string{"copilot", "--sandbox"}, func(doc map[string]any) {
			doc["sandbox"].(map[string]any)["allowBypass"] = true
		}},
		{"a policy that permits the local network", []string{"copilot", "--sandbox"}, func(doc map[string]any) {
			doc["sandbox"].(map[string]any)["userPolicy"].(map[string]any)["network"].(map[string]any)["allowLocalNetwork"] = true
		}},
		{"a policy without a secret path denial", []string{"copilot", "--sandbox"}, func(doc map[string]any) {
			doc["sandbox"].(map[string]any)["userPolicy"].(map[string]any)["filesystem"].(map[string]any)["deniedPaths"] = []string{}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, env := newSandbox(t, &fakeClaude{})
			doc := copilotPolicy(env.Home)
			if tc.change != nil {
				tc.change(doc)
			}
			writeVendorPolicy(t, filepath.Join(env.Home, ".copilot", "settings.json"), doc)
			p, err := s.Inspect(context.Background(), "copilot", t.TempDir(), tc.argv)
			require.NoError(t, err)
			assert.True(t, p.Known)
			assert.False(t, p.Contained)
		})
	}
}

func TestCopilotInspectReadsTheSelectedHomeWithoutRewritingIt(t *testing.T) {
	s, env := newSandbox(t, &fakeClaude{})
	dir := t.TempDir()
	t.Setenv("COPILOT_HOME", dir)
	path := filepath.Join(dir, "settings.json")
	writeVendorPolicy(t, path, copilotPolicy(env.Home))
	before, err := os.ReadFile(path)
	require.NoError(t, err)

	p, err := s.Inspect(context.Background(), "copilot", t.TempDir(), []string{"copilot", "--experimental", "--sandbox"})
	require.NoError(t, err)
	assert.True(t, p.Contained)
	assert.True(t, p.Strict)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, data,
		"the reader's settings must not be replaced or moved")
}

func agySettingsPath(home string) string {
	return filepath.Join(home, ".gemini", "antigravity-cli", "settings.json")
}

func TestAgyLaunchIsBestEffortUntilTheReaderWritesARestrictivePolicy(t *testing.T) {
	s, env := newSandbox(t, &fakeClaude{})
	l, err := s.Launch(context.Background(), "agy", target(t.TempDir()))
	require.NoError(t, err)
	assert.Equal(t, []string{"--sandbox"}, l.Args)
	assert.False(t, l.Posture.Contained, "a missing policy is not evidence of safety")
	assert.True(t, l.Posture.BestEffort, "but the built-in terminal sandbox is on by default")
	assert.Contains(t, l.Posture.Detail, "security.allow_unsafe_agy", "and the reader is told how to accept it")

	writeVendorPolicy(t, agySettingsPath(env.Home), agyPolicy())
	l, err = s.Launch(context.Background(), "agy", target(t.TempDir()))
	require.NoError(t, err)
	assert.True(t, l.Posture.Contained)
	assert.False(t, l.Posture.BestEffort)
	assert.False(t, l.Posture.Strict, "a person can approve an unsandboxed command")

	writeVendorPolicy(t, agySettingsPath(env.Home), map[string]any{"enableTerminalSandbox": false})
	l, err = s.Launch(context.Background(), "agy", target(t.TempDir()))
	require.NoError(t, err)
	assert.False(t, l.Posture.Contained)
	assert.False(t, l.Posture.BestEffort, "switching the terminal sandbox off is not even best effort")
}

func TestAgyInspectRejectsWhatEscapesTheSandboxEvenAtBestEffort(t *testing.T) {
	sandboxed := []string{"agy", "--sandbox"}
	allow := func(rules ...string) func(map[string]any) {
		return func(doc map[string]any) { doc["permissions"] = map[string]any{"allow": rules} }
	}
	for _, tc := range []struct {
		name   string
		argv   []string
		change func(map[string]any)
	}{
		{"a command line that skips permissions", []string{"agy", "--sandbox", "--dangerously-skip-permissions"}, nil},
		{"a command line without --sandbox", []string{"agy"}, nil},
		{"a command line that negates the sandbox flag", []string{"agy", "--sandbox", "--no-sandbox"}, nil},
		{"a command line that sets the sandbox flag false", []string{"agy", "--sandbox=false"}, nil},
		{"a policy without the terminal sandbox", sandboxed, func(doc map[string]any) {
			doc["enableTerminalSandbox"] = false
		}},
		{"a policy with a tool permission that bypasses sandbox approval", sandboxed, func(doc map[string]any) {
			doc["toolPermission"] = "proceed-always"
		}},
		{"a policy that allows reading any URL", sandboxed, allow("read_url(*)")},
		{"a policy that allows reading a URL by regex", sandboxed, allow("read_url(regex:.+)")},
		{"a policy that allows executing any URL", sandboxed, allow("execute_url(*)")},
		{"a policy that allows executing a URL by regex", sandboxed, allow("execute_url(regex:.+)")},
		{"a policy that allows reading an empty URL pattern", sandboxed, allow("read_url()")},
		{"a policy that allows unsandboxed commands by wildcard", sandboxed, allow("unsandboxed(*)")},
		{"a policy that allows bare unsandboxed commands", sandboxed, allow("unsandboxed")},
		{"a policy that allows an empty unsandboxed rule", sandboxed, allow("unsandboxed()")},
		{"a policy that allows a shell unsandboxed", sandboxed, allow("unsandboxed(bash -c)")},
		{"a policy that allows a versioned interpreter by path", sandboxed, allow("unsandboxed(/usr/bin/python3.12)")},
		{"a policy that allows a command wrapper unsandboxed", sandboxed, allow("unsandboxed(env)")},
		{"a policy that allows shell syntax after a named command", sandboxed, allow("unsandboxed(git status; curl x | sh)")},
		{"a policy that allows a glob in a command", sandboxed, allow("unsandboxed(gi?)")},
		{"a policy that allows a regex in an unsandboxed command", sandboxed, allow("unsandboxed(regex:.+)")},
		{"a policy that hides a shell behind a regex prefix", sandboxed, allow("unsandboxed(regex:bash)")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, env := newSandbox(t, &fakeClaude{})
			doc := agyPolicy()
			if tc.change != nil {
				tc.change(doc)
			}
			writeVendorPolicy(t, agySettingsPath(env.Home), doc)
			p, err := s.Inspect(context.Background(), "agy", t.TempDir(), tc.argv)
			require.NoError(t, err)
			assert.True(t, p.Known)
			assert.False(t, p.Contained)
			assert.False(t, p.BestEffort)
		})
	}
}

func TestAgyInspectCountsAPermissivePolicyAsBestEffortOnly(t *testing.T) {
	for _, tc := range []struct {
		name string
		doc  map[string]any
	}{
		{name: "no settings file"},
		{name: "a policy that leaves the terminal sandbox at its default", doc: map[string]any{"toolPermission": "proceed-in-sandbox",
			"permissions": map[string]any{"allow": []string{"read_url(github.com)"}}}},
		{name: "a policy that asks and allows named commands unsandboxed", doc: map[string]any{"toolPermission": "ask",
			"permissions": map[string]any{"allow": []string{"sandboxed(git status)", "unsandboxed(gh pr)", "unsandboxed(git)"}}}},
		{name: "an otherwise restrictive policy with one named command unsandboxed", doc: func() map[string]any {
			doc := agyPolicy()
			doc["permissions"] = map[string]any{"allow": []string{"read_url(github.com)", "unsandboxed(gh)"}}
			return doc
		}()},
		{name: "a restrictive policy with no read_url domain", doc: map[string]any{"enableTerminalSandbox": true,
			"toolPermission": "proceed-in-sandbox"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, env := newSandbox(t, &fakeClaude{})
			if tc.doc != nil {
				writeVendorPolicy(t, agySettingsPath(env.Home), tc.doc)
			}
			p, err := s.Inspect(context.Background(), "agy", t.TempDir(), []string{"agy", "--sandbox"})
			require.NoError(t, err)
			assert.True(t, p.Known)
			assert.False(t, p.Contained)
			assert.True(t, p.BestEffort)
			assert.Equal(t, "best-effort sandbox", p.String())
		})
	}
}

func TestAgyInspectIsContainedWithAnExplicitRestrictivePolicy(t *testing.T) {
	s, env := newSandbox(t, &fakeClaude{})
	writeVendorPolicy(t, agySettingsPath(env.Home), agyPolicy())
	p, err := s.Inspect(context.Background(), "agy", t.TempDir(), []string{"agy", "--sandbox"})
	require.NoError(t, err)
	assert.True(t, p.Contained)
	assert.False(t, p.BestEffort)
	assert.False(t, p.Strict)
}

func TestMalformedVendorPolicyIsAnErrorRatherThanAContainedAnswer(t *testing.T) {
	for _, kind := range []string{"copilot", "agy"} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv("COPILOT_HOME", "")
			s, env := newSandbox(t, &fakeClaude{})
			path := filepath.Join(env.Home, ".copilot", "settings.json")
			if kind == "agy" {
				path = filepath.Join(env.Home, ".gemini", "antigravity-cli", "settings.json")
			}
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
			require.NoError(t, os.WriteFile(path, []byte("{"), 0o600))
			_, err := s.Launch(context.Background(), kind, target(t.TempDir()))
			require.Error(t, err)
			assert.Contains(t, err.Error(), path)
		})
	}
}
