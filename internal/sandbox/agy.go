package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// agy has no status command and no launch-time policy, so prutil can only
// read its launch flags and the reader's own settings file, and it reaches two
// verdicts from them.
//
// Contained needs an explicit restrictive policy: --sandbox on the command
// line, the terminal sandbox switched on, tool permission limited to the
// sandbox, read_url limited to named domains, and no unsandboxed rule at all.
// A missing or permissive settings file is not evidence of safety.
//
// BestEffort is the looser reading for readers who opt in with
// security.allow_unsafe_agy. agy runs terminal commands in its built-in
// sandbox by default (verified on 1.2.11 / 1.4.9), so a missing file passes,
// and so do unsandboxed rules for named commands, which agy needs to reach the
// network at all. A rule naming a shell, an interpreter or a command wrapper,
// or any glob or regular expression, still fails, because it lets the agent
// run anything outside the sandbox. Those that pass are not safe either: git
// and gh can both run an arbitrary command. That is what the opt-in accepts.
func agy() Profile {
	return Profile{Kind: "agy", Bin: "agy", Launch: agyLaunch, Inspect: agyInspect}
}

func agyLaunch(_ context.Context, env Env, _ Runner, _ Target) (Launch, error) {
	args := []string{"--sandbox"}
	p, err := agyPosture(env, args)
	if err != nil {
		return Launch{}, err
	}
	return Launch{Args: args, Posture: p}, nil
}

func agyInspect(_ context.Context, env Env, _ Runner, _ string, argv []string) (Posture, error) {
	return agyPosture(env, argv)
}

// agySettings is the part of ~/.gemini/antigravity-cli/settings.json that
// decides the posture. EnableTerminalSandbox is a pointer so that leaving it
// out, which keeps agy's default, can be told apart from switching it off.
type agySettings struct {
	EnableTerminalSandbox *bool  `json:"enableTerminalSandbox"`
	ToolPermission        string `json:"toolPermission"`
	Permissions           struct {
		Allow []string `json:"allow"`
	} `json:"permissions"`
}

func agyPosture(env Env, argv []string) (Posture, error) {
	if why := agyFlags(sessionFlags(argv)); why != "" {
		return Posture{Known: true, Detail: why}, nil
	}
	var settings agySettings
	found := true
	path := filepath.Join(env.Home, ".gemini", "antigravity-cli", "settings.json")
	if err := readVendorSettings(path, &settings); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return Posture{}, err
		}
		found = false
	}
	if why := agyBestEffort(settings); why != "" {
		return Posture{Known: true, Detail: why}, nil
	}
	if why := agyStrict(settings, found); why != "" {
		return Posture{Known: true, BestEffort: true,
			Detail: why + "; best-effort sandbox only (security.allow_unsafe_agy)"}, nil
	}
	return Posture{Known: true, Contained: true,
		Detail: "agy --sandbox and reader policy (no vendor status command)"}, nil
}

// agyFlags says what is wrong with the command line, or nothing. --sandbox is
// required whatever else is found, and any other spelling of a sandbox flag
// fails, so that a negated form cannot read as the one prutil asked for.
func agyFlags(flags []string) string {
	if slices.Contains(flags, "--dangerously-skip-permissions") {
		return "agy was launched with --dangerously-skip-permissions"
	}
	for _, f := range flags {
		if f != "--sandbox" && strings.Contains(strings.ToLower(f), "sandbox") {
			return "agy was launched with " + f
		}
	}
	if !slices.Contains(flags, "--sandbox") {
		return "agy was not launched with --sandbox"
	}
	return ""
}

// agyBestEffort says why the settings fail even the best-effort reading, or
// nothing.
func agyBestEffort(s agySettings) string {
	if s.EnableTerminalSandbox != nil && !*s.EnableTerminalSandbox {
		return "agy terminal sandbox is off"
	}
	switch s.ToolPermission {
	case "", "ask", "proceed-in-sandbox":
	default:
		return "agy tool permission bypasses sandbox approval"
	}
	for _, rule := range s.Permissions.Allow {
		rule = strings.TrimSpace(rule)
		if rule == "unsandboxed" {
			return "agy policy runs every command unsandboxed"
		}
		if inner, ok := ruleArgument(rule, "unsandboxed"); ok {
			if why := unsandboxedEscape(inner); why != "" {
				return "agy policy allows " + rule + " (" + why + ")"
			}
			continue
		}
		for _, tool := range []string{"read_url", "execute_url"} {
			if inner, ok := ruleArgument(rule, tool); ok && (inner == "" || hasGlob(inner) || hasRegex(inner)) {
				return "agy policy allows " + rule + " (any URL)"
			}
		}
	}
	return ""
}

// agyStrict says why the settings fall short of an explicit restrictive
// policy, or nothing.
func agyStrict(s agySettings, found bool) string {
	if !found {
		return "agy sandbox policy is missing"
	}
	if s.EnableTerminalSandbox == nil || !*s.EnableTerminalSandbox {
		return "agy policy does not switch the terminal sandbox on"
	}
	if s.ToolPermission != "proceed-in-sandbox" {
		return "agy tool permission is not proceed-in-sandbox"
	}
	domains := false
	for _, rule := range s.Permissions.Allow {
		rule = strings.TrimSpace(rule)
		if _, ok := ruleArgument(rule, "unsandboxed"); ok {
			return "agy policy allows " + rule
		}
		if inner, ok := ruleArgument(rule, "read_url"); ok && inner != "" {
			domains = true
		}
	}
	if !domains {
		return "agy has no restricted read_url domain rules"
	}
	return ""
}

// ruleArgument returns what is inside tool(...), and whether rule has that
// shape at all.
func ruleArgument(rule, tool string) (string, bool) {
	inner, ok := strings.CutPrefix(rule, tool+"(")
	if !ok {
		return "", false
	}
	inner, ok = strings.CutSuffix(inner, ")")
	if !ok {
		return "", false
	}
	return strings.TrimSpace(inner), true
}

func hasGlob(s string) bool {
	return strings.ContainsAny(s, "*?[")
}

func hasRegex(s string) bool {
	return strings.HasPrefix(strings.ToLower(s), "regex:")
}

// escapeCommands run whatever they are given, so allowing one unsandboxed
// allows every command unsandboxed.
var escapeCommands = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "fish": true, "dash": true, "ksh": true,
	"csh": true, "tcsh": true, "pwsh": true, "powershell": true, "cmd": true,
	"python": true, "node": true, "deno": true, "bun": true, "perl": true,
	"ruby": true, "php": true, "lua": true, "osascript": true,
	"env": true, "xargs": true, "sudo": true, "doas": true, "exec": true,
	"eval": true, "nohup": true, "nice": true, "time": true, "timeout": true,
	"watch": true, "find": true, "awk": true, "gawk": true, "make": true,
	"npx": true, "uvx": true, "command": true, "builtin": true,
}

// unsandboxedEscape says why an unsandboxed(...) argument amounts to running
// anything, or nothing. The first word is compared by its base name with any
// version suffix taken off, so /usr/bin/python3.12 is python.
func unsandboxedEscape(inner string) string {
	if inner == "" {
		return "no command named"
	}
	if hasRegex(inner) {
		return "a regular expression"
	}
	if hasGlob(inner) {
		return "a glob"
	}
	if strings.ContainsAny(inner, ";&|`$<>\n") {
		return "shell syntax"
	}
	first := strings.Fields(inner)[0]
	name := strings.TrimRight(filepath.Base(first), "0123456789.")
	if escapeCommands[name] {
		return name + " runs any command"
	}
	return ""
}
