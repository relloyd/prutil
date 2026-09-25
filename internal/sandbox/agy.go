package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// agy 1.2.11 accepts --sandbox but has no status command or launch policy.
// A missing or permissive user-owned settings file is not evidence of safety.
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

func agyPosture(env Env, argv []string) (Posture, error) {
	flags := sessionFlags(argv)
	if !slices.Contains(flags, "--sandbox") || slices.Contains(flags, "--dangerously-skip-permissions") {
		return Posture{Known: true, Detail: "agy was not launched with safe sandbox permissions"}, nil
	}
	var settings struct {
		EnableTerminalSandbox bool   `json:"enableTerminalSandbox"`
		ToolPermission        string `json:"toolPermission"`
		Permissions           struct {
			Allow []string `json:"allow"`
		} `json:"permissions"`
	}
	path := filepath.Join(env.Home, ".gemini", "antigravity-cli", "settings.json")
	if err := readVendorSettings(path, &settings); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Posture{Known: true, Detail: "agy sandbox policy is missing"}, nil
		}
		return Posture{}, err
	}
	if !settings.EnableTerminalSandbox || settings.ToolPermission != "proceed-in-sandbox" {
		return Posture{Known: true, Detail: "agy terminal sandbox or sandbox-only tool permission is off"}, nil
	}
	domains := false
	for _, rule := range settings.Permissions.Allow {
		rule = strings.TrimSpace(rule)
		if strings.HasPrefix(rule, "unsandboxed(") || rule == "unsandboxed" ||
			(strings.HasPrefix(rule, "read_url(") || strings.HasPrefix(rule, "execute_url(")) &&
				strings.Contains(rule, "*") {
			return Posture{Known: true, Detail: "agy policy permits an unrestricted tool"}, nil
		}
		if strings.HasPrefix(rule, "read_url(") && strings.HasSuffix(rule, ")") &&
			len(rule) > len("read_url()") {
			domains = true
		}
	}
	if !domains {
		return Posture{Known: true, Detail: "agy has no restricted read_url domain rules"}, nil
	}
	return Posture{Known: true, Contained: true,
		Detail: "agy --sandbox and reader policy (no vendor status command)"}, nil
}
