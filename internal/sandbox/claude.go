package sandbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/relloyd/prutil/internal/home"
)

// Claude Code's profile. Every fact it relies on was checked against Claude
// Code 2.1.282 on macOS, with herdr 0.8.2, by starting a real agent with the
// policy below and having it run probes from inside its sandbox; the findings
// are recorded in docs/security/prompt-injection.md under Tier 2.
//
// The reader owns claudePolicy, which prutil writes once and never again. What
// cannot live there is written into a launch file per start instead: the SSH
// agent's socket, which on macOS moves every time the machine boots, and the
// git rules for the pull request's own repository.
const (
	claudePolicy = "claude-settings.json"
	launchDir    = "launch"
	// launchKeep is how long a launch file outlives its last use. An agent
	// reads its settings when it starts, so this only has to cover the agents
	// still running, and a pull request seldom takes a fortnight.
	launchKeep = 14 * 24 * time.Hour
	// statusTimeout bounds one question to claude. It answers from settings
	// files and does not touch the network.
	statusTimeout = 20 * time.Second
)

func claude() Profile {
	return Profile{Kind: "claude", Bin: "claude", Launch: claudeLaunch, Inspect: claudeInspect}
}

// claudeLaunch writes the launch file for one agent and asks Claude Code what
// that file gives the agent, from the directory it will run in, before any
// agent is started. The answer is Claude's own, not prutil's reading of the
// file, so a policy the reader has edited into something that no longer
// sandboxes is caught here rather than trusted.
func claudeLaunch(ctx context.Context, env Env, cli Runner, t Target) (Launch, error) {
	if env.Dir == "" {
		return Launch{}, fmt.Errorf("no directory to keep the Claude policy in")
	}
	defaults, err := claudeDefaultPolicy(env)
	if err != nil {
		return Launch{}, err
	}
	policyPath := filepath.Join(env.Dir, claudePolicy)
	policy, err := home.CreateOnce(policyPath, defaults)
	if err != nil {
		return Launch{}, err
	}
	rendered, err := renderClaude(policy, env, t)
	if err != nil {
		return Launch{}, fmt.Errorf("%s: %w", policyPath, err)
	}
	path, err := writeLaunchFile(env.Dir, "claude", rendered)
	if err != nil {
		return Launch{}, err
	}
	posture, err := claudeStatus(ctx, cli, t.Dir, path)
	if err != nil {
		return Launch{}, err
	}
	return Launch{Args: []string{"--settings", path}, Posture: posture}, nil
}

// claudeInspect asks Claude Code about an agent already running: with the
// settings file it was launched with, when its command line names one, and
// from its own directory, so that the project's settings count as they do for
// the agent. An agent prutil started names a launch file, which is never
// rewritten, so the answer is about what that agent actually read.
func claudeInspect(ctx context.Context, env Env, cli Runner, dir string, argv []string) (Posture, error) {
	return claudeStatus(ctx, cli, dir, settingsArg(argv))
}

// claudeDefaultPolicy is the policy prutil writes the first time it starts a
// Claude agent. It is JSON rather than code so that the reader can change it
// without rebuilding prutil; claudeLaunch checks whatever they leave there.
func claudeDefaultPolicy(env Env) ([]byte, error) {
	policy := map[string]any{
		"$schema": "https://json.schemastore.org/claude-code-settings.json",
		"sandbox": map[string]any{
			"enabled": true,
			// Strict mode: the model cannot ask its way out of the sandbox.
			// A --settings file setting this also makes Claude Code ignore
			// the sandbox values in the repository's own .claude settings,
			// which is the branch under review.
			"allowUnsandboxedCommands": false,
			// An agent working unattended cannot stop at a permission dialog:
			// herdr reports it blocked and prutil gives up on it.
			"autoAllowBashIfSandboxed": true,
			"network": map[string]any{
				"allowedDomains": []string{
					"github.com", "api.github.com", "*.githubusercontent.com",
					"proxy.golang.org", "sum.golang.org",
				},
				// gh reads its token from the keychain (SecurityServer), and
				// Go verifies TLS through trustd, so without these gh cannot
				// reply on a thread. This is the widening the design accepts
				// until Tier 3b takes the credentials out of the sandbox.
				"allowMachLookup": []string{
					"com.apple.SecurityServer", "com.apple.trustd.agent", "com.apple.trustd",
				},
			},
		},
		"permissions": map[string]any{
			// A Read or Edit deny rule also reaches the sandbox's own read and
			// write lists, so each of these confines shell commands as well as
			// Claude's file tools. The sandbox allows reads almost everywhere
			// by default, which is why they are needed at all.
			//
			// ~/.config/gh is left readable: gh cannot work without it, and
			// the token itself is in the keychain regardless. A Bash(...) rule
			// for herdr is left out because it matches only the command typed,
			// so a script walks past it; the herdr socket is kept out of reach
			// by the sandbox instead, because it is not in allowUnixSockets.
			"deny": []string{
				"Read(~/.ssh/id_*)",
				"Read(~/.aws/**)",
				"Read(~/.gnupg/**)",
				"Read(~/.netrc)",
				"Read(" + ruleGlob(env, filepath.Dir(env.Dir)) + ")",
				"Read(~/.config/herdr/**)",
				"Edit(.github/workflows/**)",
				"Edit(.claude/**)",
				"Edit(.mcp.json)",
			},
		},
	}
	out, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// ruleGlob writes a directory as the recursive path a permission rule wants:
// relative to ~ when it is in the reader's home, and absolute otherwise, which
// Claude Code spells with a leading //.
func ruleGlob(env Env, dir string) string {
	if env.Home != "" {
		if rel, err := filepath.Rel(env.Home, dir); err == nil && !strings.HasPrefix(rel, "..") {
			return "~/" + filepath.ToSlash(rel) + "/**"
		}
	}
	return "/" + filepath.ToSlash(dir) + "/**"
}

// renderClaude adds to the reader's policy what only a launch can know.
func renderClaude(policy []byte, env Env, t Target) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(policy, &doc); err != nil {
		return nil, fmt.Errorf("not a JSON object: %w", err)
	}
	if doc == nil {
		return nil, fmt.Errorf("not a JSON object")
	}
	sandbox := objectAt(doc, "sandbox")
	network := objectAt(sandbox, "network")

	// The SSH agent. The sandbox matches the real path of a socket, and on
	// macOS SSH_AUTH_SOCK is under /var, which is a symbolic link to
	// /private/var, so both are listed.
	if sock := env.SSHAuthSock; sock != "" {
		sockets := []string{sock}
		if real, err := env.realPath(sock); err == nil && real != sock {
			sockets = append(sockets, real)
		}
		network["allowUnixSockets"] = addStrings(network["allowUnixSockets"], sockets...)
	}

	if t.Host != "" && t.Owner != "" {
		if !hostPattern.MatchString(t.Host) || !ownerPattern.MatchString(t.Owner) {
			return nil, fmt.Errorf("refusing to write git rules for %q/%q", t.Host, t.Owner)
		}
		// An enterprise install is somewhere other than github.com.
		network["allowedDomains"] = addStrings(network["allowedDomains"], t.Host)
		addGitOverHTTPS(objectAt(doc, "env"), t.Host, t.Owner)
	}

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

var (
	hostPattern  = regexp.MustCompile(`^[A-Za-z0-9.-]+(:[0-9]+)?$`)
	ownerPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
)

// addGitOverHTTPS sends the agent's git traffic for the pull request's owner
// over HTTPS, with gh as the credential helper, for this session only.
//
// SSH cannot get out of Claude Code's sandbox on macOS: the sandbox's SOCKS
// proxy requires authentication, and the ProxyCommand it gives SSH there
// (nc -X 5) offers none. HTTPS goes through its HTTP proxy, which does. The
// rules travel as GIT_CONFIG_COUNT entries in the session's environment, so
// the reader's own git configuration is never touched.
//
// They have to beat the reader's rules. git rewrites a URL once, using the
// longest matching prefix, and a reader who sends https://github.com/ to SSH
// has a rule as long as any host-level one prutil could write, so the owner's
// own prefix is what wins: for all three ways a remote can be spelt, and for
// pushInsteadOf as well, which takes precedence for pushes. Other owners'
// repositories get the host-level SSH rules only, and stay on SSH wherever the
// reader's configuration says so; the agent seldom needs them.
func addGitOverHTTPS(envBlock map[string]any, host, owner string) {
	https := "https://" + host + "/"
	own := https + owner + "/"
	pairs := [][2]string{}
	for _, form := range []string{"git@" + host + ":", "ssh://git@" + host + "/", https} {
		pairs = append(pairs,
			[2]string{"url." + own + ".insteadOf", form + owner + "/"},
			[2]string{"url." + own + ".pushInsteadOf", form + owner + "/"},
		)
	}
	for _, form := range []string{"git@" + host + ":", "ssh://git@" + host + "/"} {
		pairs = append(pairs,
			[2]string{"url." + https + ".insteadOf", form},
			[2]string{"url." + https + ".pushInsteadOf", form},
		)
	}
	// The empty helper first clears any the reader configured for the host,
	// so git asks gh rather than a keychain helper it cannot use here.
	pairs = append(pairs,
		[2]string{"credential." + strings.TrimSuffix(https, "/") + ".helper", ""},
		[2]string{"credential." + strings.TrimSuffix(https, "/") + ".helper", "!gh auth git-credential"},
	)

	// Entries a reader already has are kept, and these follow them.
	n := 0
	if count, ok := envBlock["GIT_CONFIG_COUNT"].(string); ok {
		n, _ = strconv.Atoi(count)
	}
	for i, pair := range pairs {
		envBlock["GIT_CONFIG_KEY_"+strconv.Itoa(n+i)] = pair[0]
		envBlock["GIT_CONFIG_VALUE_"+strconv.Itoa(n+i)] = pair[1]
	}
	envBlock["GIT_CONFIG_COUNT"] = strconv.Itoa(n + len(pairs))
}

// objectAt returns the object stored under key, creating it when it is
// missing or is not an object.
func objectAt(parent map[string]any, key string) map[string]any {
	if existing, ok := parent[key].(map[string]any); ok {
		return existing
	}
	created := map[string]any{}
	parent[key] = created
	return created
}

// addStrings appends values a list does not already hold, keeping whatever
// the reader put there first.
func addStrings(list any, values ...string) []any {
	out, _ := list.([]any)
	for _, v := range values {
		if !slices.ContainsFunc(out, func(got any) bool { return got == v }) {
			out = append(out, v)
		}
	}
	return out
}

// writeLaunchFile stores a rendered policy under a name taken from its
// contents. An agent reads its settings when it starts and a running agent's
// command line names the file, so a file is never rewritten with different
// contents: asking about that agent later reads what it actually read.
func writeLaunchFile(dir, kind string, data []byte) (string, error) {
	sum := sha256.Sum256(data)
	path := filepath.Join(dir, launchDir, kind+"-"+hex.EncodeToString(sum[:8])+".json")
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, data) {
		now := time.Now()
		_ = os.Chtimes(path, now, now)
		return path, nil
	}
	if err := home.WriteFile(path, data); err != nil {
		return "", err
	}
	pruneLaunchFiles(filepath.Dir(path), path)
	return path, nil
}

// pruneLaunchFiles removes launch files nothing has used for launchKeep. A new
// one appears whenever the SSH socket moves, which is every boot.
func pruneLaunchFiles(dir, keep string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		if path == keep || e.IsDir() || filepath.Ext(path) != ".json" {
			continue
		}
		if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > launchKeep {
			_ = os.Remove(path)
		}
	}
}

// settingsArg finds the --settings a command line names, in either spelling.
func settingsArg(argv []string) string {
	for i := 1; i < len(argv); i++ {
		switch {
		case argv[i] == "--settings" && i+1 < len(argv):
			return argv[i+1]
		case strings.HasPrefix(argv[i], "--settings="):
			return strings.TrimPrefix(argv[i], "--settings=")
		}
	}
	return ""
}

// claudeSandboxStatus is the one JSON line claude sandbox status prints.
// statusVersion 3 is what 2.1.282 prints; the fields read here are the ones
// that say whether commands are sandboxed and whether the model can leave.
type claudeSandboxStatus struct {
	StatusVersion     int     `json:"statusVersion"`
	Supported         *bool   `json:"supported"`
	Enabled           bool    `json:"enabled"`
	EnabledSource     string  `json:"enabledSource"`
	StrictMode        bool    `json:"strictMode"`
	UnavailableReason *string `json:"unavailableReason"`
}

// claudeStatus asks claude sandbox status, from dir, with settings when it is
// given. It reads settings files and nothing else.
func claudeStatus(ctx context.Context, cli Runner, dir, settings string) (Posture, error) {
	args := []string{"sandbox", "status"}
	if settings != "" {
		args = append([]string{"--settings", settings}, args...)
	}
	ctx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	out, err := cli.RunIn(ctx, dir, args...)
	if err != nil {
		return Posture{}, fmt.Errorf("could not ask claude about its sandbox: %w", err)
	}
	return parseClaudeStatus(out)
}

// parseClaudeStatus reads the status line, tolerating anything printed
// before it.
func parseClaudeStatus(out []byte) (Posture, error) {
	for _, line := range bytes.Split(out, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var st claudeSandboxStatus
		if err := json.Unmarshal(line, &st); err != nil {
			continue
		}
		p := Posture{Known: true, Strict: st.StrictMode}
		switch {
		case st.Supported != nil && !*st.Supported:
			reason := "not supported here"
			if st.UnavailableReason != nil && *st.UnavailableReason != "" {
				reason += ": " + *st.UnavailableReason
			}
			p.Detail = "claude sandbox " + reason
		case st.Enabled:
			p.Contained = true
			p.Detail = "claude sandbox on, from " + nonEmpty(st.EnabledSource, "settings")
			if st.StrictMode {
				p.Detail += ", strict"
			}
		default:
			p.Detail = "claude sandbox off"
		}
		return p, nil
	}
	first, _, _ := bytes.Cut(bytes.TrimSpace(out), []byte("\n"))
	return Posture{}, fmt.Errorf("claude sandbox status said %q, not the JSON line prutil reads", first)
}

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
