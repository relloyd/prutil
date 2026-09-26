package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Copilot has no status command or launch-time policy file. Its --sandbox
// switch is experimental in 1.0.88, and the reader owns the settings file.
func copilot() Profile {
	return Profile{Kind: "copilot", Bin: "copilot", Launch: copilotLaunch, Inspect: copilotInspect}
}

func copilotLaunch(_ context.Context, env Env, _ Runner, t Target) (Launch, error) {
	args := []string{"--experimental", "--sandbox",
		"--secret-env-vars=AWS_ACCESS_KEY_ID,AWS_SECRET_ACCESS_KEY,AWS_SESSION_TOKEN"}
	p, err := copilotPosture(env, args)
	if err != nil {
		return Launch{}, err
	}
	return Launch{Args: args, Posture: p}, nil
}

func copilotInspect(_ context.Context, env Env, _ Runner, _ string, argv []string) (Posture, error) {
	return copilotPosture(env, argv)
}

func copilotPosture(env Env, argv []string) (Posture, error) {
	flags := sessionFlags(argv)
	if !slices.Contains(flags, "--sandbox") || !slices.Contains(flags, "--experimental") ||
		slices.Contains(flags, "--no-sandbox") ||
		slices.Contains(flags, "--no-experimental") {
		return Posture{Known: true, Detail: "copilot was not launched with its sandbox enabled"}, nil
	}
	dir := filepath.Join(env.Home, ".copilot")
	if custom := os.Getenv("COPILOT_HOME"); custom != "" {
		dir = custom
	}
	var settings struct {
		Sandbox struct {
			AllowBypass *bool `json:"allowBypass"`
			UserPolicy  struct {
				Filesystem struct {
					DeniedPaths []string `json:"deniedPaths"`
				} `json:"filesystem"`
				Network struct {
					AllowLocalNetwork *bool `json:"allowLocalNetwork"`
				} `json:"network"`
			} `json:"userPolicy"`
		} `json:"sandbox"`
	}
	if err := readVendorSettings(filepath.Join(dir, "settings.json"), &settings); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Posture{Known: true, Detail: "copilot sandbox policy is missing"}, nil
		}
		return Posture{}, err
	}
	s := settings.Sandbox
	if s.AllowBypass == nil || *s.AllowBypass ||
		s.UserPolicy.Network.AllowLocalNetwork == nil || *s.UserPolicy.Network.AllowLocalNetwork {
		return Posture{Known: true, Detail: "copilot sandbox bypass or local network access is not disabled"}, nil
	}
	for _, path := range []string{".ssh", ".aws", ".gnupg", ".netrc", ".config/herdr", ".config/prutil"} {
		if !slices.Contains(s.UserPolicy.Filesystem.DeniedPaths, filepath.Join(env.Home, path)) {
			return Posture{Known: true, Detail: "copilot sandbox does not deny " + path}, nil
		}
	}
	return Posture{Known: true, Contained: true, Strict: true,
		Detail: "copilot --sandbox and reader policy (no vendor status command)"}, nil
}

func readVendorSettings(path string, settings any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	if len(strings.TrimSpace(string(data))) == 0 || strings.TrimSpace(string(data)) == "null" {
		return fmt.Errorf("%s: expected a JSON object", path)
	}
	if err := json.Unmarshal(data, settings); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// sessionFlags excludes prompt text: an agent launched with -p "--sandbox"
// has not enabled a sandbox, even though its argv contains the same word.
func sessionFlags(argv []string) []string {
	for i, arg := range argv {
		switch arg {
		case "--", "-p", "--prompt", "-i", "--prompt-interactive", "--print":
			return argv[:i]
		}
	}
	return argv
}
