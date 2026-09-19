package home

import (
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// NotificationEvent names a change to a pull request that prutil can raise a
// desktop notification for. It is also the key the event is stored under in
// config.yaml, beneath notifications.events.
type NotificationEvent string

// The events prutil can notify about. Adding one means a constant here, an
// entry in notificationDefaults, and a rule in the UI that recognises it.
const (
	// NotifyApproved is a pull request becoming approved.
	NotifyApproved NotificationEvent = "approved"
)

// notificationDefaults is every event, in the order the settings pane lists
// them, with whether it is on before anybody says otherwise.
var notificationDefaults = []struct {
	event NotificationEvent
	on    bool
}{
	{NotifyApproved, true},
}

// NotificationEvents lists every event prutil can notify about, in the order
// the settings pane shows them.
func NotificationEvents() []NotificationEvent {
	out := make([]NotificationEvent, 0, len(notificationDefaults))
	for _, d := range notificationDefaults {
		out = append(out, d.event)
	}
	return out
}

// Known reports whether prutil has a notification for the event, which a key
// somebody typed into the configuration file need not be.
func (e NotificationEvent) Known() bool {
	return slices.Contains(NotificationEvents(), e)
}

// defaultOn is whether the event notifies when the configuration is silent.
func (e NotificationEvent) defaultOn() bool {
	for _, d := range notificationDefaults {
		if d.event == e {
			return d.on
		}
	}
	return false
}

// DefaultNotificationInterval is how often every open pull request is read
// for the changes a notification is raised on. An approval a couple of
// minutes late is still news; a request every few seconds for every pull
// request the reader has open would be a poor trade for it.
const DefaultNotificationInterval = 2 * time.Minute

// NotificationConfig governs the desktop notifications prutil raises itself.
// They are separate from herdr.toast, which is the notification herdr shows
// when prutil hands work to an agent.
type NotificationConfig struct {
	// Interval is how often every open pull request is read while any
	// notification is on. Watched pull requests are also read on the
	// watcher's own schedule, which is often sooner.
	Interval Duration `yaml:"interval"`
	// Events turns each notification on or off. An event the file does not
	// mention takes its default.
	Events map[NotificationEvent]bool `yaml:"events"`
}

// defaultNotifications returns the built-in settings in a map of their own.
//
// Fresh every call for the same reason as defaultMarker: the YAML decoder
// writes into a map it finds in the target, so a shared one would let one
// file change the defaults for the whole process.
func defaultNotifications() NotificationConfig {
	events := make(map[NotificationEvent]bool, len(notificationDefaults))
	for _, d := range notificationDefaults {
		events[d.event] = d.on
	}
	return NotificationConfig{Interval: Duration(DefaultNotificationInterval), Events: events}
}

// Enabled reports whether a change of this kind raises a notification.
func (n NotificationConfig) Enabled(e NotificationEvent) bool {
	if on, ok := n.Events[e]; ok {
		return on
	}
	return e.defaultOn()
}

// Any reports whether any notification is on, which is what decides whether
// prutil reads the open pull requests for them at all.
func (n NotificationConfig) Any() bool {
	for _, e := range NotificationEvents() {
		if n.Enabled(e) {
			return true
		}
	}
	return false
}

// Set turns one notification on or off in memory. SetNotification is what
// makes the change outlive the session.
func (n *NotificationConfig) Set(e NotificationEvent, on bool) {
	if n.Events == nil {
		n.Events = map[NotificationEvent]bool{}
	}
	n.Events[e] = on
}

// notificationPath is where an event's setting lives in config.yaml.
func notificationPath(e NotificationEvent) []string {
	return []string{"notifications", "events", string(e)}
}

// SetNotification writes one notification setting into config.yaml, and
// nothing else. The file is the reader's own, often written by hand, so the
// value is changed where it stands, or added beside its siblings, and every
// comment, blank line and other setting is left exactly as it was.
//
// It refuses rather than guesses. A configuration that does not parse is not
// touched, and neither is one whose shape the edit cannot be sure of: the
// result is read back before it is written, and anything besides this one
// setting reading differently is an error.
func (s *Store) SetNotification(event NotificationEvent, on bool) error {
	if !event.Known() {
		return fmt.Errorf("prutil has no %q notification", event)
	}
	return s.SaveSetting(notificationPath(event), strconv.FormatBool(on), func(c *Config) {
		if c.Notifications.Events == nil {
			c.Notifications.Events = map[NotificationEvent]bool{}
		} else {
			c.Notifications.Events = maps.Clone(c.Notifications.Events)
		}
		c.Notifications.Set(event, on)
	})
}

// SetWatchSelfReview writes the watch.self_review setting into config.yaml,
// and nothing else.
func (s *Store) SetWatchSelfReview(on bool) error {
	return s.SaveSetting([]string{"watch", "self_review"}, strconv.FormatBool(on), func(c *Config) {
		c.Watch.SelfReview = on
	})
}

// dotted writes a configuration path the way the documentation does.
func dotted(path []string) string { return strings.Join(path, ".") }

// replaceFile swaps a file's contents for data in one step, by writing a
// sibling and renaming it over the original. A crash part way through leaves
// the old file whole rather than a truncated one.
//
// The sibling is written beside the file itself, so a path that is a symbolic
// link, as a dotfiles manager makes, should be resolved first: renaming over
// the link would replace it with a copy.
func replaceFile(path string, data []byte, perm fs.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return fmt.Errorf("could not create %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("could not write %s: %w", path, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("could not write %s: %w", path, err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("could not write %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("could not write %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("could not write %s: %w", path, err)
	}
	return nil
}
