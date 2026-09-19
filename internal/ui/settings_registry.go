package ui

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/relloyd/prutil/internal/home"
)

// settingKind describes the interactive control type for a setting.
type settingKind int

const (
	settingKindBool settingKind = iota
	settingKindEnum
	settingKindDuration
	settingKindInt
	settingKindString
	settingKindTemplate
	settingKindMap
	settingKindList
)

// settingDescriptor defines a configurable setting in the settings panel.
//
// Adding a new setting to prutil:
//  1. Add the field to the appropriate Config struct in internal/home/config.go.
//  2. Add an entry to allSettings in this file with its section, title, detail, kind,
//     path, accessors, and validators.
//
// The settings pane will automatically display, navigate, edit, and persist it.
type settingDescriptor struct {
	id          string
	section     string
	title       string
	detail      string
	defaultText string
	kind        settingKind
	enumValues  []string
	path        []string

	minDuration  time.Duration
	stepDuration time.Duration
	minInt       int
	stepInt      int

	// getDisplay returns the formatted value string shown on the right side of the row.
	getDisplay func(a *App) string
	// getRaw returns the editable string used to pre-fill inline textinput.
	getRaw func(a *App) string
	// isDefault reports whether the setting currently equals its built-in default.
	isDefault func(a *App) bool
	// isEnabled reports whether the boolean setting is on (for settingKindBool).
	isEnabled func(a *App) bool
	// toggle flips a boolean setting and saves it.
	toggle func(a *App) tea.Cmd
	// cycle advances or retreats an enum setting and saves it.
	cycle func(a *App, delta int) tea.Cmd
	// step increments or decrements a duration or int setting and saves it.
	step func(a *App, delta int) tea.Cmd
	// saveInput validates and saves an inline textinput entry.
	saveInput func(a *App, input string) error
	// reset reverts the setting to its built-in default in config.yaml and memory.
	reset func(a *App) error
}

// defCfg is what every setting compares itself against to say whether it is at
// its default. Built once, because isDefault is asked on every frame.
var defCfg = home.DefaultConfig()

// field is read and write access to one value in the configuration.
//
// It is all a descriptor needs to know about where its setting lives: the
// display, the comparison against the default, the save and the reset all
// follow from being able to get the value and set it. Before this, each of
// those was a closure of its own naming the same config field four or five
// times over, which is how one of them came to be written in a different
// order from the rest.
type field[T comparable] struct {
	get func(c *home.Config) T
	set func(c *home.Config, v T)
}

// settingMeta is everything about a setting except where its value lives.
type settingMeta struct {
	id      string
	section string
	title   string
	detail  string
	def     string
	path    []string
	// label names the setting in the notice a change raises, when that is not
	// the title. Several read better shortened: "Max poll backoff cap" is a
	// heading, "Max poll backoff set to 30m" is a sentence.
	label string
	// verb is how a boolean words itself in a notice, when "is" does not fit.
	verb string
	// after runs once a change has been saved, for the settings something else
	// depends on: rereading reviews, or rescheduling notifications.
	after func(a *App) tea.Cmd
}

// verbOrIs is how a boolean setting reads in its notice. "is" suits a singular
// label; a plural one such as "Herdr toast notifications" takes "are".
func (m settingMeta) verbOrIs() string {
	if m.verb != "" {
		return m.verb
	}
	return "is"
}

func (m settingMeta) noticeLabel() string {
	if m.label != "" {
		return m.label
	}
	return m.title
}

// run is the after hook, or nothing when the setting has none.
func (m settingMeta) run(a *App) tea.Cmd {
	if m.after == nil {
		return nil
	}
	return m.after(a)
}

// saved reports a change that reached the file, and failed one that did not.
func (m settingMeta) saved(a *App, text string) {
	a.settings.setNotice(text+" · saved", false)
}

func (m settingMeta) failed(a *App, err error) {
	a.settings.setNotice("could not save "+strings.ToLower(m.noticeLabel())+": "+err.Error(), true)
}

// base is the half of a descriptor that says what a setting is rather than
// what it does, which every kind fills in the same way.
func (m settingMeta) base(kind settingKind) settingDescriptor {
	return settingDescriptor{
		id:          m.id,
		section:     m.section,
		title:       m.title,
		detail:      m.detail,
		defaultText: m.def,
		kind:        kind,
		path:        m.path,
	}
}

// resetTo returns the reset every kind shares: take the key out of the file so
// the built-in default applies again, and move the running configuration with
// it.
func resetTo[T comparable](m settingMeta, f field[T]) func(a *App) error {
	return func(a *App) error {
		return a.resetSetting(m.path, func(c *home.Config) { f.set(c, f.get(&defCfg)) })
	}
}

// isDefaultOf reports whether a setting still holds the value it would have if
// it were not in the file at all.
func isDefaultOf[T comparable](f field[T]) func(a *App) bool {
	return func(a *App) bool { return f.get(&a.homeCfg) == f.get(&defCfg) }
}

// durationSetting is a setting holding a length of time, stepped with + and -
// or typed in exactly. min is the shortest it will accept, which is a courtesy
// to GitHub rather than a limitation of the field.
func durationSetting(m settingMeta, f field[home.Duration], min, step time.Duration) settingDescriptor {
	save := func(a *App, d time.Duration) error {
		if err := a.saveSetting(m.path, d.String(), func(c *home.Config) { f.set(c, home.Duration(d)) }); err != nil {
			return err
		}
		m.saved(a, fmt.Sprintf("%s set to %s", m.noticeLabel(), d))
		return nil
	}
	shown := func(a *App) string { return f.get(&a.homeCfg).String() }

	d := m.base(settingKindDuration)
	d.minDuration, d.stepDuration = min, step
	d.getDisplay, d.getRaw = shown, shown
	d.isDefault = isDefaultOf(f)
	d.reset = resetTo(m, f)
	d.step = func(a *App, delta int) tea.Cmd {
		next := max(time.Duration(f.get(&a.homeCfg))+time.Duration(delta)*step, min)
		if err := save(a, next); err != nil {
			m.failed(a, err)
			return nil
		}
		return m.run(a)
	}
	d.saveInput = func(a *App, input string) error {
		v, err := time.ParseDuration(strings.TrimSpace(input))
		if err != nil {
			return fmt.Errorf("not a duration such as 30s or 1m: %w", err)
		}
		if v < min {
			return fmt.Errorf("minimum interval is %s", min)
		}
		if err := save(a, v); err != nil {
			return err
		}
		m.run(a)
		return nil
	}
	return d
}

// boolSetting is a setting that is on or off, flipped with space or x.
func boolSetting(m settingMeta, f field[bool]) settingDescriptor {
	d := m.base(settingKindBool)
	d.getDisplay = func(a *App) string { return onOff(f.get(&a.homeCfg)) }
	d.getRaw = func(a *App) string { return strconv.FormatBool(f.get(&a.homeCfg)) }
	d.isEnabled = func(a *App) bool { return f.get(&a.homeCfg) }
	d.isDefault = isDefaultOf(f)
	d.reset = resetTo(m, f)
	d.toggle = func(a *App) tea.Cmd {
		next := !f.get(&a.homeCfg)
		if err := a.saveSetting(m.path, strconv.FormatBool(next), func(c *home.Config) { f.set(c, next) }); err != nil {
			m.failed(a, err)
			return nil
		}
		m.saved(a, fmt.Sprintf("%s %s %s", m.noticeLabel(), m.verbOrIs(), onOff(next)))
		return m.run(a)
	}
	return d
}

// intSetting is a whole number, stepped with + and - or typed in exactly.
// format is how the number reads on the row, such as "%d polls", and the notice
// reuses it so the two cannot drift apart.
func intSetting(m settingMeta, f field[int], min, step int, format string) settingDescriptor {
	show := func(n int) string { return fmt.Sprintf(format, n) }
	save := func(a *App, n int) error {
		if err := a.saveSetting(m.path, strconv.Itoa(n), func(c *home.Config) { f.set(c, n) }); err != nil {
			return err
		}
		m.saved(a, fmt.Sprintf("%s set to %s", m.noticeLabel(), show(n)))
		return nil
	}

	d := m.base(settingKindInt)
	d.minInt, d.stepInt = min, step
	d.getDisplay = func(a *App) string { return show(f.get(&a.homeCfg)) }
	d.getRaw = func(a *App) string { return strconv.Itoa(f.get(&a.homeCfg)) }
	d.isDefault = isDefaultOf(f)
	d.reset = resetTo(m, f)
	d.step = func(a *App, delta int) tea.Cmd {
		if err := save(a, max(f.get(&a.homeCfg)+delta*step, min)); err != nil {
			m.failed(a, err)
			return nil
		}
		return m.run(a)
	}
	d.saveInput = func(a *App, input string) error {
		n, err := strconv.Atoi(strings.TrimSpace(input))
		if err != nil {
			return fmt.Errorf("not a whole number: %w", err)
		}
		if n < min {
			return fmt.Errorf("minimum is %d", min)
		}
		if err := save(a, n); err != nil {
			return err
		}
		m.run(a)
		return nil
	}
	return d
}

// allSettings returns the complete registry of settings displayed in the pane,
// grouped by their section headers.
func allSettings() []settingDescriptor {

	return []settingDescriptor{
		// ---------------------------------------------------------------------
		// DESKTOP NOTIFICATIONS
		// ---------------------------------------------------------------------
		{
			id:          "notifications.approved",
			section:     "DESKTOP NOTIFICATIONS",
			title:       "Pull request approved",
			detail:      "When one of your open pull requests is approved: its review decision turns to approved or, in a repository without review rules, it gets its first approval.",
			defaultText: "on",
			kind:        settingKindBool,
			path:        []string{"notifications", "events", "approved"},
			getDisplay: func(a *App) string {
				if a.homeCfg.Notifications.Enabled(home.NotifyApproved) {
					return "on"
				}
				return "off"
			},
			getRaw: func(a *App) string {
				return strconv.FormatBool(a.homeCfg.Notifications.Enabled(home.NotifyApproved))
			},
			isEnabled: func(a *App) bool {
				return a.homeCfg.Notifications.Enabled(home.NotifyApproved)
			},
			isDefault: func(a *App) bool {
				return a.homeCfg.Notifications.Enabled(home.NotifyApproved) == defCfg.Notifications.Enabled(home.NotifyApproved)
			},
			toggle: func(a *App) tea.Cmd {
				on := !a.homeCfg.Notifications.Enabled(home.NotifyApproved)
				a.homeCfg.Notifications.Set(home.NotifyApproved, on)
				state := "off"
				if on {
					state = "on"
				}
				if a.store != nil {
					if err := a.store.SetNotification(home.NotifyApproved, on); err != nil {
						a.settings.setNotice(fmt.Sprintf("Pull request approved is %s until prutil quits, but was not saved: %s", state, err), true)
					} else if on && a.settings.unavailable != "" {
						a.settings.setNotice(fmt.Sprintf("Pull request approved is on and saved, but nothing will appear: %s", a.settings.unavailable), true)
					} else {
						a.settings.setNotice(fmt.Sprintf("Pull request approved is %s · saved", state), false)
					}
				} else if a.storeErr != nil {
					a.settings.setNotice(fmt.Sprintf("Pull request approved is %s until prutil quits, but was not saved: %s", state, a.storeErr), true)
				} else {
					a.settings.setNotice(fmt.Sprintf("Pull request approved is %s", state), false)
				}
				return a.scheduleNotifications()
			},
			reset: func(a *App) error {
				return a.setNotification(home.NotifyApproved, defCfg.Notifications.Enabled(home.NotifyApproved))
			},
		},
		durationSetting(settingMeta{
			id:      "notifications.interval",
			section: "DESKTOP NOTIFICATIONS",
			title:   "Check poll interval",
			detail:  "How often prutil checks your open pull requests in the background while any notification is enabled.",
			def:     "2m",
			path:    []string{"notifications", "interval"},
			after:   func(a *App) tea.Cmd { return a.scheduleNotifications() },
		}, field[home.Duration]{
			get: func(c *home.Config) home.Duration { return c.Notifications.Interval },
			set: func(c *home.Config, v home.Duration) { c.Notifications.Interval = v },
		}, 15*time.Second, 30*time.Second),

		// ---------------------------------------------------------------------
		// WATCHING & POLLING
		// ---------------------------------------------------------------------
		boolSetting(settingMeta{
			id:      "watch.self_review",
			section: "WATCHING & POLLING",
			title:   "Self-review feedback",
			detail: "Treat every unresolved review comment written from your account as actionable feedback for " +
				"coding agents, apart from the replies your agents left behind.",
			def:   "off",
			path:  []string{"watch", "self_review"},
			after: func(a *App) tea.Cmd { return a.rereadArmedReviews() },
		}, field[bool]{
			get: func(c *home.Config) bool { return c.Watch.SelfReview },
			set: func(c *home.Config, v bool) { c.Watch.SelfReview = v },
		}),
		durationSetting(settingMeta{
			id:      "watch.active_interval",
			section: "WATCHING & POLLING",
			title:   "Active poll interval",
			detail:  "How often an armed pull request is polled while checks or workflows are actively running.",
			def:     "30s",
			path:    []string{"watch", "active_interval"},
		}, field[home.Duration]{
			get: func(c *home.Config) home.Duration { return c.Watch.ActiveInterval },
			set: func(c *home.Config, v home.Duration) { c.Watch.ActiveInterval = v },
		}, 15*time.Second, 15*time.Second),
		durationSetting(settingMeta{
			id:      "watch.base_interval",
			section: "WATCHING & POLLING",
			title:   "Base poll interval",
			detail:  "Starting polling interval for an armed pull request once all checks have finished running.",
			def:     "2m",
			path:    []string{"watch", "base_interval"},
		}, field[home.Duration]{
			get: func(c *home.Config) home.Duration { return c.Watch.BaseInterval },
			set: func(c *home.Config, v home.Duration) { c.Watch.BaseInterval = v },
		}, 15*time.Second, 30*time.Second),
		durationSetting(settingMeta{
			id:      "watch.max_interval",
			section: "WATCHING & POLLING",
			title:   "Max poll backoff cap",
			detail:  "Maximum polling backoff interval reached when an armed pull request remains unchanged.",
			def:     "30m",
			path:    []string{"watch", "max_interval"},
			label:   "Max poll backoff",
		}, field[home.Duration]{
			get: func(c *home.Config) home.Duration { return c.Watch.MaxInterval },
			set: func(c *home.Config, v home.Duration) { c.Watch.MaxInterval = v },
		}, 15*time.Second, 5*time.Minute),
		durationSetting(settingMeta{
			id:      "watch.notified_interval",
			section: "WATCHING & POLLING",
			title:   "Post-handoff interval",
			detail:  "Initial polling interval after handing review feedback to a coding agent.",
			def:     "10m",
			path:    []string{"watch", "notified_interval"},
		}, field[home.Duration]{
			get: func(c *home.Config) home.Duration { return c.Watch.NotifiedInterval },
			set: func(c *home.Config, v home.Duration) { c.Watch.NotifiedInterval = v },
		}, 15*time.Second, 1*time.Minute),
		durationSetting(settingMeta{
			id:      "watch.max_notified_interval",
			section: "WATCHING & POLLING",
			title:   "Max post-handoff cap",
			detail:  "Maximum polling backoff cap after handing review feedback to a coding agent.",
			def:     "60m",
			path:    []string{"watch", "max_notified_interval"},
		}, field[home.Duration]{
			get: func(c *home.Config) home.Duration { return c.Watch.MaxNotifiedInterval },
			set: func(c *home.Config, v home.Duration) { c.Watch.MaxNotifiedInterval = v },
		}, 15*time.Second, 5*time.Minute),
		durationSetting(settingMeta{
			id:      "watch.idle_interval",
			section: "WATCHING & POLLING",
			title:   "Agent idle check interval",
			detail:  "How often a target agent is polled over local socket while waiting for it to become idle.",
			def:     "10s",
			path:    []string{"watch", "idle_interval"},
			label:   "Agent idle interval",
		}, field[home.Duration]{
			get: func(c *home.Config) home.Duration { return c.Watch.IdleInterval },
			set: func(c *home.Config, v home.Duration) { c.Watch.IdleInterval = v },
		}, 1*time.Second, 5*time.Second),
		intSetting(settingMeta{
			id:      "watch.dormant_after",
			section: "WATCHING & POLLING",
			title:   "Dormant poll threshold",
			detail:  "How many consecutive polls at max interval with no changes before polling goes dormant.",
			def:     "3 polls",
			path:    []string{"watch", "dormant_after"},
			label:   "Dormant threshold",
		}, field[int]{
			get: func(c *home.Config) int { return c.Watch.DormantAfter },
			set: func(c *home.Config, v int) { c.Watch.DormantAfter = v },
		}, 1, 1, "%d polls"),
		intSetting(settingMeta{
			id:      "watch.force_precise_every",
			section: "WATCHING & POLLING",
			title:   "Force precise check every",
			detail:  "Poll count interval to run the precise review-thread query regardless of tripwire counters.",
			def:     "5 polls",
			path:    []string{"watch", "force_precise_every"},
			label:   "Force precise check",
		}, field[int]{
			get: func(c *home.Config) int { return c.Watch.ForcePreciseEvery },
			set: func(c *home.Config, v int) { c.Watch.ForcePreciseEvery = v },
		}, 1, 1, "every %d polls"),
		{
			id:          "watch.self_test_marker",
			section:     "WATCHING & POLLING",
			title:       "Self-test marker comment",
			detail:      "Review comment marker string treated as feedback when written by yourself. Empty to disable.",
			defaultText: "[prutil-test]",
			kind:        settingKindString,
			path:        []string{"watch", "self_test_marker"},
			getDisplay: func(a *App) string {
				m := a.homeCfg.Watch.Marker()
				if m == "" {
					return "(disabled)"
				}
				return m
			},
			getRaw: func(a *App) string {
				return a.homeCfg.Watch.Marker()
			},
			isDefault: func(a *App) bool {
				return a.homeCfg.Watch.Marker() == defCfg.Watch.Marker()
			},
			saveInput: func(a *App, input string) error {
				marker := strings.TrimSpace(input)
				if err := a.saveSetting([]string{"watch", "self_test_marker"}, strconv.Quote(marker), func(c *home.Config) {
					c.Watch.SelfTestMarker = &marker
				}); err != nil {
					return err
				}
				if marker == "" {
					a.settings.setNotice("Self-test marker disabled · saved", false)
				} else {
					a.settings.setNotice(fmt.Sprintf("Self-test marker set to %q · saved", marker), false)
				}
				return nil
			},
			reset: func(a *App) error {
				return a.resetSetting([]string{"watch", "self_test_marker"}, func(c *home.Config) {
					c.Watch.SelfTestMarker = defCfg.Watch.SelfTestMarker
				})
			},
		},

		// ---------------------------------------------------------------------
		// AI REVIEW TRIGGER
		// ---------------------------------------------------------------------
		{
			id:          "review.comment",
			section:     "AI REVIEW TRIGGER",
			title:       "Default review comment",
			detail:      "Comment posted to an open pull request when pressing R to trigger an AI review. Empty to disable.",
			defaultText: "/gemini review",
			kind:        settingKindString,
			path:        []string{"review", "comment"},
			getDisplay: func(a *App) string {
				c := a.homeCfg.Review.CommentFor("")
				if c == "" {
					return "(disabled)"
				}
				return c
			},
			getRaw: func(a *App) string {
				return a.homeCfg.Review.CommentFor("")
			},
			isDefault: func(a *App) bool {
				return a.homeCfg.Review.CommentFor("") == defCfg.Review.CommentFor("")
			},
			saveInput: func(a *App, input string) error {
				comment := strings.TrimSpace(input)
				if err := a.saveSetting([]string{"review", "comment"}, strconv.Quote(comment), func(c *home.Config) {
					c.Review.Comment = &comment
				}); err != nil {
					return err
				}
				if comment == "" {
					a.settings.setNotice("Review comment trigger disabled · saved", false)
				} else {
					a.settings.setNotice(fmt.Sprintf("Review comment set to %q · saved", comment), false)
				}
				return nil
			},
			reset: func(a *App) error {
				return a.resetSetting([]string{"review", "comment"}, func(c *home.Config) {
					c.Review.Comment = defCfg.Review.Comment
				})
			},
		},
		{
			id:          "review.repos",
			section:     "AI REVIEW TRIGGER",
			title:       "Repository comment overrides",
			detail:      "Per-repository review comment overrides (e.g. owner/repo -> @coderabbitai review). Press enter to manage.",
			defaultText: "0 overrides",
			kind:        settingKindMap,
			path:        []string{"review", "repos"},
			getDisplay: func(a *App) string {
				n := len(a.homeCfg.Review.Repos)
				if n == 1 {
					return "1 override"
				}
				return fmt.Sprintf("%d overrides", n)
			},
			getRaw: func(a *App) string {
				return ""
			},
			isDefault: func(a *App) bool {
				return len(a.homeCfg.Review.Repos) == 0
			},
		},

		// ---------------------------------------------------------------------
		// CODING AGENT (HERDR)
		// ---------------------------------------------------------------------
		{
			id:          "herdr.fallback",
			section:     "CODING AGENT (HERDR)",
			title:       "Fallback provisioning strategy",
			detail:      "Fallback strategy when no active agent matches PR branch: 'new' (provision fresh workspace), 'none' (take no action), 'repo' (use any agent in repository).",
			defaultText: "new",
			kind:        settingKindEnum,
			enumValues:  []string{"new", "none", "repo"},
			path:        []string{"herdr", "fallback"},
			getDisplay: func(a *App) string {
				return fmt.Sprintf("« %s »", a.homeCfg.Herdr.Fallback)
			},
			getRaw: func(a *App) string {
				return string(a.homeCfg.Herdr.Fallback)
			},
			isDefault: func(a *App) bool {
				return a.homeCfg.Herdr.Fallback == defCfg.Herdr.Fallback
			},
			cycle: func(a *App, delta int) tea.Cmd {
				choices := []home.FallbackStrategy{home.FallbackNew, home.FallbackNone, home.FallbackRepo}
				idx := slices.Index(choices, a.homeCfg.Herdr.Fallback)
				if idx < 0 {
					idx = 0
				}
				nextIdx := (idx + delta) % len(choices)
				if nextIdx < 0 {
					nextIdx += len(choices)
				}
				next := choices[nextIdx]
				valStr := string(next)
				if err := a.saveSetting([]string{"herdr", "fallback"}, valStr, func(c *home.Config) {
					c.Herdr.Fallback = next
				}); err != nil {
					a.settings.setNotice("could not save fallback: "+err.Error(), true)
					return nil
				}
				a.settings.setNotice(fmt.Sprintf("Fallback strategy set to %q · saved", next), false)
				return nil
			},
			reset: func(a *App) error {
				return a.resetSetting([]string{"herdr", "fallback"}, func(c *home.Config) {
					c.Herdr.Fallback = defCfg.Herdr.Fallback
				})
			},
		},
		{
			id:          "herdr.agent_kind",
			section:     "CODING AGENT (HERDR)",
			title:       "Agent kind filter",
			detail:      "Restrict handoffs to one kind of agent (such as 'claude' or 'copilot'). Empty allows any agent.",
			defaultText: "(any)",
			kind:        settingKindString,
			path:        []string{"herdr", "agent_kind"},
			getDisplay: func(a *App) string {
				if a.homeCfg.Herdr.AgentKind == "" {
					return "(any)"
				}
				return a.homeCfg.Herdr.AgentKind
			},
			getRaw: func(a *App) string {
				return a.homeCfg.Herdr.AgentKind
			},
			isDefault: func(a *App) bool {
				return a.homeCfg.Herdr.AgentKind == defCfg.Herdr.AgentKind
			},
			saveInput: func(a *App, input string) error {
				kind := strings.TrimSpace(input)
				if err := a.saveSetting([]string{"herdr", "agent_kind"}, strconv.Quote(kind), func(c *home.Config) {
					c.Herdr.AgentKind = kind
				}); err != nil {
					return err
				}
				if kind == "" {
					a.settings.setNotice("Agent kind filter cleared (any agent allowed) · saved", false)
				} else {
					a.settings.setNotice(fmt.Sprintf("Agent kind filter set to %q · saved", kind), false)
				}
				return nil
			},
			reset: func(a *App) error {
				return a.resetSetting([]string{"herdr", "agent_kind"}, func(c *home.Config) {
					c.Herdr.AgentKind = defCfg.Herdr.AgentKind
				})
			},
		},
		{
			id:          "herdr.skill",
			section:     "CODING AGENT (HERDR)",
			title:       "Herdr skill name",
			detail:      "Skill name invoked in the default prompt when handing off review feedback (e.g. 'triage').",
			defaultText: "(none)",
			kind:        settingKindString,
			path:        []string{"herdr", "skill"},
			getDisplay: func(a *App) string {
				if a.homeCfg.Herdr.Skill == "" {
					return "(none)"
				}
				return a.homeCfg.Herdr.Skill
			},
			getRaw: func(a *App) string {
				return a.homeCfg.Herdr.Skill
			},
			isDefault: func(a *App) bool {
				return a.homeCfg.Herdr.Skill == defCfg.Herdr.Skill
			},
			saveInput: func(a *App, input string) error {
				skill := strings.TrimSpace(input)
				if err := a.saveSetting([]string{"herdr", "skill"}, strconv.Quote(skill), func(c *home.Config) {
					c.Herdr.Skill = skill
				}); err != nil {
					return err
				}
				if skill == "" {
					a.settings.setNotice("Herdr skill cleared · saved", false)
				} else {
					a.settings.setNotice(fmt.Sprintf("Herdr skill set to %q · saved", skill), false)
				}
				return nil
			},
			reset: func(a *App) error {
				return a.resetSetting([]string{"herdr", "skill"}, func(c *home.Config) {
					c.Herdr.Skill = defCfg.Herdr.Skill
				})
			},
		},
		durationSetting(settingMeta{
			id:      "herdr.wait_for_idle",
			section: "CODING AGENT (HERDR)",
			title:   "Wait for agent idle timeout",
			detail:  "How long prutil waits for a working agent to settle before falling back to a notification.",
			def:     "15m",
			path:    []string{"herdr", "wait_for_idle"},
			label:   "Wait for idle timeout",
		}, field[home.Duration]{
			get: func(c *home.Config) home.Duration { return c.Herdr.WaitForIdle },
			set: func(c *home.Config, v home.Duration) { c.Herdr.WaitForIdle = v },
		}, 0, 1*time.Minute),
		boolSetting(settingMeta{
			id:      "herdr.dry_run",
			section: "CODING AGENT (HERDR)",
			title:   "Dry run mode",
			detail:  "Record handoffs in logs and notifications without actually sending them to the agent.",
			def:     "off",
			path:    []string{"herdr", "dry_run"},
		}, field[bool]{
			get: func(c *home.Config) bool { return c.Herdr.DryRun },
			set: func(c *home.Config, v bool) { c.Herdr.DryRun = v },
		}),
		boolSetting(settingMeta{
			id:      "herdr.toast",
			section: "CODING AGENT (HERDR)",
			title:   "Herdr toast notifications",
			detail:  "Show herdr's desktop notification alongside each agent handoff.",
			def:     "on",
			path:    []string{"herdr", "toast"},
			verb:    "are",
		}, field[bool]{
			get: func(c *home.Config) bool { return c.Herdr.Toast },
			set: func(c *home.Config, v bool) { c.Herdr.Toast = v },
		}),
		{
			id:          "herdr.prompt",
			section:     "CODING AGENT (HERDR)",
			title:       "Review handoff prompt",
			detail:      "Template rendered with Repo, Number, URL, Title, HeadRef, BaseRef, UnresolvedCount, Note. Press enter to preview, e to edit in $EDITOR.",
			defaultText: "(default template)",
			kind:        settingKindTemplate,
			path:        []string{"herdr", "prompt"},
			getDisplay: func(a *App) string {
				if a.homeCfg.Herdr.Prompt == home.DefaultPrompt {
					return "(default)"
				}
				return "(custom template)"
			},
			getRaw: func(a *App) string {
				return a.homeCfg.Herdr.Prompt
			},
			isDefault: func(a *App) bool {
				return a.homeCfg.Herdr.Prompt == home.DefaultPrompt
			},
			reset: func(a *App) error {
				return a.resetSetting([]string{"herdr", "prompt"}, func(c *home.Config) {
					c.Herdr.Prompt = home.DefaultPrompt
				})
			},
		},
		{
			id:          "herdr.check_prompt",
			section:     "CODING AGENT (HERDR)",
			title:       "Failed checks prompt",
			detail:      "Template rendered for failed-check investigations with Repo, Number, URL, Checks, Note. Press enter to preview, e to edit in $EDITOR.",
			defaultText: "(default template)",
			kind:        settingKindTemplate,
			path:        []string{"herdr", "check_prompt"},
			getDisplay: func(a *App) string {
				if a.homeCfg.Herdr.CheckPrompt == home.DefaultCheckPrompt {
					return "(default)"
				}
				return "(custom template)"
			},
			getRaw: func(a *App) string {
				return a.homeCfg.Herdr.CheckPrompt
			},
			isDefault: func(a *App) bool {
				return a.homeCfg.Herdr.CheckPrompt == home.DefaultCheckPrompt
			},
			reset: func(a *App) error {
				return a.resetSetting([]string{"herdr", "check_prompt"}, func(c *home.Config) {
					c.Herdr.CheckPrompt = home.DefaultCheckPrompt
				})
			},
		},

		// ---------------------------------------------------------------------
		// REPOSITORIES & DISCOVERY
		// ---------------------------------------------------------------------
		{
			id:          "repos",
			section:     "REPOSITORIES & DISCOVERY",
			title:       "Explicit repository paths",
			detail:      "Map GitHub repository (owner/name) to explicit local checkout directory path. Press enter to manage.",
			defaultText: "0 paths",
			kind:        settingKindMap,
			path:        []string{"repos"},
			getDisplay: func(a *App) string {
				n := len(a.homeCfg.Repos)
				if n == 1 {
					return "1 path"
				}
				return fmt.Sprintf("%d paths", n)
			},
			getRaw: func(a *App) string {
				return ""
			},
			isDefault: func(a *App) bool {
				return len(a.homeCfg.Repos) == 0
			},
		},
		{
			id:          "discovery.roots",
			section:     "REPOSITORIES & DISCOVERY",
			title:       "Checkout discovery roots",
			detail:      "Root directories scanned to discover git checkouts by matching remote origin URLs. Press enter to manage.",
			defaultText: "0 roots",
			kind:        settingKindList,
			path:        []string{"discovery", "roots"},
			getDisplay: func(a *App) string {
				n := len(a.homeCfg.Discovery.Roots)
				if n == 1 {
					return "1 root"
				}
				return fmt.Sprintf("%d roots", n)
			},
			getRaw: func(a *App) string {
				return ""
			},
			isDefault: func(a *App) bool {
				return len(a.homeCfg.Discovery.Roots) == 0
			},
		},
	}
}

// onOff returns "on" for true and "off" for false.
func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// setNotification turns one desktop notification on or off, in the file first
// and then in the running configuration. The store owns the change it makes to
// the events map, so unlike the others this one names it here too.
func (a *App) setNotification(event home.NotificationEvent, on bool) error {
	return a.applySetting(func(c *home.Config) {
		if c.Notifications.Events == nil {
			c.Notifications.Events = map[home.NotificationEvent]bool{}
		} else {
			c.Notifications.Events = maps.Clone(c.Notifications.Events)
		}
		c.Notifications.Set(event, on)
	}, func() error { return a.store.SetNotification(event, on) })
}

// setWatchSelfReview turns self-review feedback on or off.
func (a *App) setWatchSelfReview(on bool) error {
	return a.applySetting(func(c *home.Config) { c.Watch.SelfReview = on },
		func() error { return a.store.SetWatchSelfReview(on) })
}

// saveSetting writes one setting into config.yaml and, once that has succeeded,
// applies the same change to the configuration prutil is running on.
//
// The order is the whole point. apply is the change expressed as a function, so
// the store can use it to say what the file ought to parse back to and this can
// use it to move the running configuration to the same place. Every descriptor
// used to assign to a.homeCfg first and save afterwards, which left a failed
// save showing a value that is not in the file — and, because a.homeCfg is what
// polls and hands off, acting on it until prutil was restarted.
func (a *App) saveSetting(path []string, value string, apply func(c *home.Config)) error {
	return a.applySetting(apply, func() error { return a.store.SaveSetting(path, value, apply) })
}

// saveBlockScalar is saveSetting for a multi-line value.
func (a *App) saveBlockScalar(path []string, text string, apply func(c *home.Config)) error {
	return a.applySetting(apply, func() error { return a.store.SaveBlockScalar(path, text, apply) })
}

// resetSetting takes a setting out of config.yaml, so that its default applies
// again, and moves the running configuration with it.
func (a *App) resetSetting(path []string, apply func(c *home.Config)) error {
	return a.applySetting(apply, func() error { return a.store.ResetSetting(path, apply) })
}

// saveMapEntry and deleteMapEntry are saveSetting for one entry of a mapping.
func (a *App) saveMapEntry(path []string, key, value string, apply func(c *home.Config)) error {
	return a.applySetting(apply, func() error { return a.store.SaveMapEntry(path, key, value, apply) })
}

func (a *App) deleteMapEntry(path []string, key string, apply func(c *home.Config)) error {
	return a.applySetting(apply, func() error { return a.store.DeleteMapEntry(path, key, apply) })
}

// saveSequence is saveSetting for a list.
func (a *App) saveSequence(path []string, items []string, apply func(c *home.Config)) error {
	return a.applySetting(apply, func() error { return a.store.SaveSequence(path, items, apply) })
}

// applySetting runs a write and applies apply to the running configuration only
// afterwards. With nowhere to write, the running configuration is all there is,
// so it applies straight away.
func (a *App) applySetting(apply func(c *home.Config), write func() error) error {
	if a.store == nil {
		apply(&a.homeCfg)
		return nil
	}
	if err := write(); err != nil {
		return err
	}
	apply(&a.homeCfg)
	return nil
}
