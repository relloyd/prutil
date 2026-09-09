package home

import (
	"fmt"
	"strings"
	"text/template"
	"time"

	"go.yaml.in/yaml/v3"
)

// DefaultPrompt is what prutil says to an agent when the configuration does
// not override it. With a skill configured it invokes that skill, because a
// slash command is how a Claude Code skill is asked for by name. Without one
// it spells the job out, so the feature works before anybody has written a
// skill for it.
const DefaultPrompt = `{{if .Skill}}/{{.Skill}} {{.URL}}{{else}}` +
	`Triage the review feedback on {{.Repo}}#{{.Number}}: {{.URL}}

It is {{.HeadRef}} into {{.BaseRef}}, with {{.UnresolvedCount}} unresolved review ` +
	`{{if eq .UnresolvedCount 1}}thread{{else}}threads{{end}}. Read each one, make the ` +
	`changes that should be made, and reply on the threads you are leaving alone saying why.` +
	`{{end}}{{if .Note}}

{{.Note}}{{end}}`

// Config is everything the application directory can be asked to remember
// about how prutil should behave. Every field is optional: an absent file, an
// empty file and a file setting one key all produce a working configuration.
type Config struct {
	Herdr HerdrConfig `yaml:"herdr"`
	Watch WatchConfig `yaml:"watch"`
}

// HerdrConfig governs what prutil says to a coding agent, and to which one.
type HerdrConfig struct {
	// AgentKind restricts handoffs to one kind of agent, such as "claude" or
	// "copilot". Empty means any agent herdr recognises will do.
	AgentKind string `yaml:"agent_kind"`
	// Skill names the triage skill the default prompt invokes.
	Skill string `yaml:"skill"`
	// Prompt is a text/template rendered with PromptData.
	Prompt string `yaml:"prompt"`
	// WaitForIdle bounds how long prutil will wait for a working agent to
	// settle before giving up and falling back to a notification.
	WaitForIdle Duration `yaml:"wait_for_idle"`
	// DryRun records what would have been sent without sending it, which is
	// how the first run of this feature should be tried.
	DryRun bool `yaml:"dry_run"`
	// Toast shows a herdr notification alongside each handoff, on the
	// assumption that the reader is looking at another workspace by then.
	Toast bool `yaml:"toast"`
}

// WatchConfig governs how hard prutil polls GitHub for an armed pull request.
// The defaults are deliberately slack: an idle pull request should cost almost
// nothing, and the interesting case, a run of checks going green, is the only
// one polled quickly.
type WatchConfig struct {
	// ActiveInterval is used while checks are still running.
	ActiveInterval Duration `yaml:"active_interval"`
	// BaseInterval starts the backoff once nothing is in progress.
	BaseInterval Duration `yaml:"base_interval"`
	// MaxInterval caps that backoff.
	MaxInterval Duration `yaml:"max_interval"`
	// NotifiedInterval starts the slower backoff used after a handoff, on the
	// grounds that an agent given work will take a while over it.
	NotifiedInterval Duration `yaml:"notified_interval"`
	// MaxNotifiedInterval caps that one.
	MaxNotifiedInterval Duration `yaml:"max_notified_interval"`
	// IdleInterval is how often a target agent is re-read while prutil waits
	// for it to stop working. It asks herdr over a local socket, not GitHub,
	// so it can afford to be short.
	IdleInterval Duration `yaml:"idle_interval"`
	// DormantAfter is how many polls at the cap, with nothing changing and no
	// checks running, end polling altogether.
	DormantAfter int `yaml:"dormant_after"`
	// ForcePreciseEvery makes prutil run the precise review-thread query every
	// nth poll regardless of the tripwire, because a reply inside an existing
	// thread moves no counter.
	ForcePreciseEvery int `yaml:"force_precise_every"`
}

// DefaultConfig is the configuration prutil uses when nothing overrides it.
func DefaultConfig() Config {
	return Config{
		Herdr: HerdrConfig{
			Prompt:      DefaultPrompt,
			WaitForIdle: Duration(15 * time.Minute),
			Toast:       true,
		},
		Watch: WatchConfig{
			ActiveInterval:      Duration(30 * time.Second),
			BaseInterval:        Duration(2 * time.Minute),
			MaxInterval:         Duration(30 * time.Minute),
			NotifiedInterval:    Duration(10 * time.Minute),
			MaxNotifiedInterval: Duration(60 * time.Minute),
			IdleInterval:        Duration(10 * time.Second),
			DormantAfter:        3,
			ForcePreciseEvery:   5,
		},
	}
}

// ParseConfig decodes a configuration file over the defaults, so that a file
// setting one key leaves every other key at its default rather than at zero.
func ParseConfig(data []byte) (Config, error) {
	cfg := DefaultConfig()
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return DefaultConfig(), fmt.Errorf("could not read the configuration: %w", err)
	}
	cfg.clamp()
	return cfg, nil
}

// minPollInterval is the shortest gap prutil will leave between two polls of
// the same pull request, whatever the configuration says. A typo in a
// configuration file should not be able to turn a dashboard into a denial of
// service against GitHub's GraphQL API.
const minPollInterval = 15 * time.Second

// clamp brings a configuration back inside the range prutil is willing to act
// on, rather than rejecting a file over a value it can simply correct.
func (c *Config) clamp() {
	if strings.TrimSpace(c.Herdr.Prompt) == "" {
		c.Herdr.Prompt = DefaultPrompt
	}
	if c.Herdr.WaitForIdle < 0 {
		c.Herdr.WaitForIdle = 0
	}

	w := &c.Watch
	floor := Duration(minPollInterval)
	for _, d := range []*Duration{
		&w.ActiveInterval, &w.BaseInterval, &w.MaxInterval,
		&w.NotifiedInterval, &w.MaxNotifiedInterval, &w.IdleInterval,
	} {
		if *d < floor {
			*d = floor
		}
	}
	// A cap below the interval it caps would make the backoff run backwards.
	w.MaxInterval = max(w.MaxInterval, w.BaseInterval)
	w.MaxNotifiedInterval = max(w.MaxNotifiedInterval, w.NotifiedInterval)

	if w.DormantAfter < 1 {
		w.DormantAfter = 1
	}
	if w.ForcePreciseEvery < 1 {
		w.ForcePreciseEvery = 1
	}
}

// PromptData is what the prompt template is rendered with.
type PromptData struct {
	Repo    string
	Number  int
	URL     string
	Title   string
	HeadRef string
	BaseRef string
	// Skill is copied from the configuration so a template can branch on it.
	Skill string
	// UnresolvedCount is every unresolved review thread on the pull request;
	// NewCount is how many of those prutil has not handed off before.
	UnresolvedCount int
	NewCount        int
	// Note carries anything prutil needs to warn the agent about, such as the
	// checkout sitting on a different branch from the pull request.
	Note string
}

// RenderPrompt turns the configured template into the text sent to an agent.
func (h HerdrConfig) RenderPrompt(data PromptData) (string, error) {
	data.Skill = h.Skill

	tmpl, err := template.New("prompt").Parse(h.Prompt)
	if err != nil {
		return "", fmt.Errorf("the configured herdr prompt is not a valid template: %w", err)
	}
	var out strings.Builder
	if err := tmpl.Execute(&out, data); err != nil {
		return "", fmt.Errorf("could not render the herdr prompt: %w", err)
	}
	text := strings.TrimSpace(out.String())
	if text == "" {
		return "", fmt.Errorf("the configured herdr prompt rendered to nothing")
	}
	return text, nil
}

// Duration is a time.Duration that reads from YAML in git's own vocabulary:
// "30s", "2m", "1h30m".
type Duration time.Duration

// Duration returns the underlying value.
func (d Duration) Duration() time.Duration { return time.Duration(d) }

// String implements fmt.Stringer.
func (d Duration) String() string { return time.Duration(d).String() }

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var text string
	if err := node.Decode(&text); err != nil {
		return fmt.Errorf("%s is not a duration such as \"2m\"", node.Value)
	}
	parsed, err := time.ParseDuration(strings.TrimSpace(text))
	if err != nil {
		return fmt.Errorf("%q is not a duration such as \"2m\"", text)
	}
	*d = Duration(parsed)
	return nil
}

// MarshalYAML implements yaml.Marshaler, so that a configuration prutil writes
// back reads the same way it went in.
func (d Duration) MarshalYAML() (any, error) { return d.String(), nil }
